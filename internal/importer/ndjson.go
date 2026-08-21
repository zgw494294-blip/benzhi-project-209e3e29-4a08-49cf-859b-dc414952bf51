package importer

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"thermoguard/internal/app"
)

const (
	DefaultMaxLineBytes = 1 << 20
	DefaultMaxReadings  = 1000
)

type Record struct {
	DeviceID       string    `json:"device_id"`
	SampledAt      time.Time `json:"sampled_at"`
	Celsius        float64   `json:"celsius"`
	Source         string    `json:"source"`
	IdempotencyKey string    `json:"idempotency_key"`
}

type LineError struct {
	Line    int    `json:"line"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *LineError) Error() string {
	return fmt.Sprintf("第 %d 行 %s", e.Line, e.Message)
}

type ParseOptions struct {
	MaxLineBytes int
	MaxReadings  int
}

func ParseNDJSON(reader io.Reader, options ParseOptions) ([]app.AddReadingInput, error) {
	if options.MaxLineBytes <= 0 {
		options.MaxLineBytes = DefaultMaxLineBytes
	}
	if options.MaxReadings <= 0 {
		options.MaxReadings = DefaultMaxReadings
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), options.MaxLineBytes)
	inputs := make([]app.AddReadingInput, 0)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			return nil, &LineError{Line: lineNumber, Code: "EMPTY_LINE", Message: "不能为空行"}
		}
		var record Record
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			code := "INVALID_JSON"
			message := "不是有效 JSON"
			if strings.Contains(err.Error(), "unknown field") {
				code = "UNKNOWN_FIELD"
				message = "包含未知字段"
			}
			return nil, &LineError{Line: lineNumber, Code: code, Message: message}
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, &LineError{Line: lineNumber, Code: "MULTIPLE_VALUES", Message: "只能包含一个 JSON 对象"}
		}
		if record.SampledAt.Location() != time.UTC {
			return nil, &LineError{Line: lineNumber, Code: "NON_UTC_TIME", Message: "sampled_at 必须使用 UTC"}
		}
		inputs = append(inputs, app.AddReadingInput{
			DeviceID: record.DeviceID, SampledAt: record.SampledAt.UTC(), Celsius: record.Celsius,
			Source: record.Source, IdempotencyKey: record.IdempotencyKey,
		})
		if len(inputs) > options.MaxReadings {
			return nil, &LineError{Line: lineNumber, Code: "TOO_MANY_READINGS", Message: fmt.Sprintf("读数数量不能超过 %d", options.MaxReadings)}
		}
	}
	if err := scanner.Err(); err != nil {
		if errorsIsTooLong(err) {
			return nil, &LineError{Line: lineNumber + 1, Code: "LINE_TOO_LARGE", Message: "单行超过大小限制"}
		}
		return nil, fmt.Errorf("读取导入文件: %w", err)
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("导入文件没有读数")
	}
	return inputs, nil
}

func errorsIsTooLong(err error) bool {
	return strings.Contains(err.Error(), "token too long")
}
