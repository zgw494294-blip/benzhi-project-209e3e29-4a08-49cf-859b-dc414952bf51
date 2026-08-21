package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"thermoguard/internal/app"
	"thermoguard/internal/domain"
)

const maxBodyBytes = 1 << 20

type Server struct {
	service     *app.Service
	mux         *http.ServeMux
	requestSeq  atomic.Uint64
	jobsHealthy func() bool
	jobsError   func() string
}

func New(service *app.Service, jobsHealthy func() bool, jobsError ...func() string) *Server {
	if jobsHealthy == nil {
		jobsHealthy = func() bool { return true }
	}
	errorStatus := func() string { return "" }
	if len(jobsError) > 0 && jobsError[0] != nil {
		errorStatus = jobsError[0]
	}
	s := &Server{service: service, mux: http.NewServeMux(), jobsHealthy: jobsHealthy, jobsError: errorStatus}
	s.routes()
	return s
}
func (s *Server) Handler() http.Handler { return s.withRequest(s.mux) }

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/v1/healthz", s.health)
	s.mux.HandleFunc("POST /api/v1/policies", s.createPolicy)
	s.mux.HandleFunc("POST /api/v1/policies/{policy_id}/publish", s.publishPolicy)
	s.mux.HandleFunc("GET /api/v1/policies/{policy_id}", s.getPolicy)
	s.mux.HandleFunc("POST /api/v1/lots", s.createLot)
	s.mux.HandleFunc("GET /api/v1/lots", s.listLots)
	s.mux.HandleFunc("GET /api/v1/lots/{lot_id}", s.getLot)
	s.mux.HandleFunc("POST /api/v1/lots/{lot_id}/readings", s.addReading)
	s.mux.HandleFunc("POST /api/v1/lots/{lot_id}/readings:batch", s.addReadings)
	s.mux.HandleFunc("GET /api/v1/lots/{lot_id}/readings", s.listReadings)
	s.mux.HandleFunc("POST /api/v1/lots/{lot_id}/monitoring:close", s.closeMonitoring)
	s.mux.HandleFunc("POST /api/v1/lots/{lot_id}/evaluate", s.evaluate)
	s.mux.HandleFunc("GET /api/v1/lots/{lot_id}/excursions", s.listExcursions)
	s.mux.HandleFunc("POST /api/v1/lots/{lot_id}/investigations", s.createInvestigation)
	s.mux.HandleFunc("POST /api/v1/investigations/{investigation_id}/evidence", s.addEvidence)
	s.mux.HandleFunc("POST /api/v1/investigations/{investigation_id}/actions", s.createAction)
	s.mux.HandleFunc("PATCH /api/v1/actions/{action_id}", s.updateAction)
	s.mux.HandleFunc("POST /api/v1/investigations/{investigation_id}/submit", s.submitInvestigation)
	s.mux.HandleFunc("GET /api/v1/lots/{lot_id}/release-preview", s.releasePreview)
	s.mux.HandleFunc("POST /api/v1/lots/{lot_id}/decisions", s.createDecision)
	s.mux.HandleFunc("GET /api/v1/lots/{lot_id}/audit", s.audit)
	s.mux.HandleFunc("GET /api/v1/lots/{lot_id}/case-export", s.exportCase)
}

type envelope struct {
	Data any `json:"data"`
	Meta any `json:"meta,omitempty"`
}
type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Details   any    `json:"details"`
	RequestID string `json:"request_id"`
}
type ctxKey int

const requestIDKey ctxKey = 1

func (s *Server) withRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := fmt.Sprintf("req-%016x", s.requestSeq.Add(1))
		ctx := contextWithRequestID(r, id)
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		start := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				log.Printf("request_id=%s panic=%v", id, recovered)
				s.writeError(w, r, errors.New("内部服务错误"))
			}
			log.Printf("request_id=%s method=%s path=%s status=done duration_ms=%d", id, r.Method, r.URL.Path, time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(w, ctx)
	})
}

func contextWithRequestID(r *http.Request, id string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), requestIDKey, id))
}
func requestID(r *http.Request) string { v, _ := r.Context().Value(requestIDKey).(string); return v }

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	h := s.service.Store().Health()
	statistics, statisticsErr := s.service.Store().Statistics()
	status := "ok"
	code := http.StatusOK
	backgroundHealthy := s.jobsHealthy()
	backgroundError := s.jobsError()
	if !h.Writable || !h.AuditValid || h.JournalTailTruncated || !backgroundHealthy || statisticsErr != nil {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}
	s.write(w, code, envelope{Data: map[string]any{"status": status, "repository": h, "statistics": statistics, "background_jobs_alive": backgroundHealthy, "background_jobs_error": backgroundError}})
}
func (s *Server) createPolicy(w http.ResponseWriter, r *http.Request) {
	var in app.CreatePolicyInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.CreatePolicy(actor(r), in)
	s.result(w, r, http.StatusCreated, out, err)
}
func (s *Server) publishPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.noBody(w, r) {
		return
	}
	out, err := s.service.PublishPolicy(actor(r), r.PathValue("policy_id"))
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request) {
	out, err := s.service.GetPolicy(r.PathValue("policy_id"))
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) createLot(w http.ResponseWriter, r *http.Request) {
	var in app.CreateLotInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.CreateLot(actor(r), in)
	s.result(w, r, http.StatusCreated, out, err)
}
func (s *Server) listLots(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit", 50)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, next, err := s.service.ListLots(domain.LotStatus(r.URL.Query().Get("status")), r.URL.Query().Get("policy_id"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.write(w, http.StatusOK, envelope{Data: out, Meta: map[string]any{"next_cursor": next}})
}
func (s *Server) getLot(w http.ResponseWriter, r *http.Request) {
	lot, preview, err := s.service.GetLot(r.PathValue("lot_id"))
	s.result(w, r, http.StatusOK, map[string]any{"lot": lot, "release_preview": preview}, err)
}
func (s *Server) addReading(w http.ResponseWriter, r *http.Request) {
	var in app.AddReadingInput
	if !s.decode(w, r, &in) {
		return
	}
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		in.IdempotencyKey = key
	}
	out, err := s.service.AddReading(actor(r), r.PathValue("lot_id"), in)
	s.result(w, r, http.StatusCreated, out, err)
}
func (s *Server) addReadings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Readings []app.AddReadingInput `json:"readings"`
	}
	if !s.decode(w, r, &body) {
		return
	}
	out, err := s.service.AddReadings(actor(r), r.PathValue("lot_id"), body.Readings)
	s.result(w, r, http.StatusCreated, out, err)
}
func (s *Server) listReadings(w http.ResponseWriter, r *http.Request) {
	from, err := optionalTime(r.URL.Query().Get("from"), "from")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	to, err := optionalTime(r.URL.Query().Get("to"), "to")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.service.ListReadings(r.PathValue("lot_id"), from, to)
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) closeMonitoring(w http.ResponseWriter, r *http.Request) {
	if !s.noBody(w, r) {
		return
	}
	out, err := s.service.CloseMonitoring(actor(r), r.PathValue("lot_id"))
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) evaluate(w http.ResponseWriter, r *http.Request) {
	if !s.noBody(w, r) {
		return
	}
	out, err := s.service.Evaluate(actor(r), r.PathValue("lot_id"))
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) listExcursions(w http.ResponseWriter, r *http.Request) {
	rawInclude := r.URL.Query().Get("include_revoked")
	if rawInclude != "" && rawInclude != "true" && rawInclude != "false" {
		s.writeError(w, r, domain.Invalid("include_revoked", "include_revoked 必须是 true 或 false"))
		return
	}
	include := rawInclude == "true"
	out, err := s.service.ListExcursions(r.PathValue("lot_id"), include)
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) createInvestigation(w http.ResponseWriter, r *http.Request) {
	var in app.CreateInvestigationInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.CreateInvestigation(actor(r), r.PathValue("lot_id"), in)
	s.result(w, r, http.StatusCreated, out, err)
}
func (s *Server) addEvidence(w http.ResponseWriter, r *http.Request) {
	var in app.AddEvidenceInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.AddEvidence(actor(r), r.PathValue("investigation_id"), in)
	s.result(w, r, http.StatusCreated, out, err)
}
func (s *Server) createAction(w http.ResponseWriter, r *http.Request) {
	var in app.CreateActionInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.CreateAction(actor(r), r.PathValue("investigation_id"), in)
	s.result(w, r, http.StatusCreated, out, err)
}
func (s *Server) updateAction(w http.ResponseWriter, r *http.Request) {
	var in app.UpdateActionInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.UpdateAction(actor(r), r.PathValue("action_id"), in)
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) submitInvestigation(w http.ResponseWriter, r *http.Request) {
	var in app.SubmitInvestigationInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.SubmitInvestigation(actor(r), r.PathValue("investigation_id"), in)
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) releasePreview(w http.ResponseWriter, r *http.Request) {
	out, err := s.service.ReleasePreview(r.PathValue("lot_id"))
	s.result(w, r, http.StatusOK, out, err)
}
func (s *Server) createDecision(w http.ResponseWriter, r *http.Request) {
	var in app.CreateDecisionInput
	if !s.decode(w, r, &in) {
		return
	}
	out, err := s.service.CreateDecision(actor(r), r.PathValue("lot_id"), in)
	s.result(w, r, http.StatusCreated, out, err)
}
func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	cursor, err := queryInt64(r, "cursor", 0)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	limit, err := queryInt(r, "limit", 100)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	events, next, valid, err := s.service.Audit(r.PathValue("lot_id"), cursor, limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.write(w, http.StatusOK, envelope{Data: events, Meta: map[string]any{"next_cursor": next, "chain_valid": valid}})
}
func (s *Server) exportCase(w http.ResponseWriter, r *http.Request) {
	out, err := s.service.Export(r.PathValue("lot_id"))
	if err == nil {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=thermoguard-%s.json", r.PathValue("lot_id")))
	}
	s.result(w, r, http.StatusOK, out, err)
}

func actor(r *http.Request) string { return strings.TrimSpace(r.Header.Get("X-Actor")) }
func (s *Server) decode(w http.ResponseWriter, r *http.Request, target any) bool {
	if actor(r) == "" {
		s.writeError(w, r, domain.Invalid("actor", "写请求必须提供 X-Actor"))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		s.writeError(w, r, decodeError(err))
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		s.writeError(w, r, domain.Invalid("body", "请求体只能包含一个 JSON 值"))
		return false
	}
	return true
}
func (s *Server) noBody(w http.ResponseWriter, r *http.Request) bool {
	if actor(r) == "" {
		s.writeError(w, r, domain.Invalid("actor", "写请求必须提供 X-Actor"))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, r, decodeError(err))
		return false
	}
	if len(strings.TrimSpace(string(data))) != 0 && string(strings.TrimSpace(string(data))) != "{}" {
		s.writeError(w, r, domain.Invalid("body", "该接口不接受请求字段"))
		return false
	}
	return true
}
func decodeError(err error) error {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return &domain.Error{Code: "BODY_TOO_LARGE", Message: "请求体超过 1 MiB"}
	}
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return domain.Invalid("body", "JSON 语法错误")
	}
	if errors.Is(err, io.EOF) {
		return domain.Invalid("body", "请求体不能为空")
	}
	if strings.Contains(err.Error(), "unknown field") {
		return domain.Invalid("body", "请求包含未知字段")
	}
	return domain.Invalid("body", "请求字段类型或格式错误")
}
func (s *Server) result(w http.ResponseWriter, r *http.Request, status int, data any, err error) {
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.write(w, status, envelope{Data: data})
}
func (s *Server) write(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	body := errorBody{Code: "INTERNAL_ERROR", Message: "内部服务错误", RequestID: requestID(r)}
	var de *domain.Error
	if errors.As(err, &de) {
		body.Code = de.Code
		body.Message = de.Message
		body.Details = de.Details
		switch de.Code {
		case "NOT_FOUND":
			status = http.StatusNotFound
		case "CONFLICT", "INVALID_STATE", "VERSION_CONFLICT":
			status = http.StatusConflict
		case "BODY_TOO_LARGE":
			status = http.StatusRequestEntityTooLarge
		default:
			status = http.StatusUnprocessableEntity
		}
	}
	s.write(w, status, body)
}
func queryInt(r *http.Request, key string, def int) (int, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return 0, domain.Invalid(key, key+" 必须是非负整数")
	}
	return v, nil
}
func queryInt64(r *http.Request, key string, def int64) (int64, error) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0, domain.Invalid(key, key+" 必须是非负整数")
	}
	return v, nil
}
func optionalTime(raw, field string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil || t.Location() != time.UTC {
		return nil, domain.Invalid(field, field+" 必须是 RFC3339 UTC 时间")
	}
	t = t.UTC()
	return &t, nil
}
