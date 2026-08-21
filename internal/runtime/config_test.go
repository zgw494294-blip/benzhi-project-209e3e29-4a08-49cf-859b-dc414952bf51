package runtime

import "testing"

func TestResolveAddressPrecedence(t *testing.T) {
	tests := []struct {
		name, flag string
		explicit   bool
		port, want string
		wantErr    bool
	}{
		{"默认值", "127.0.0.1:19081", false, "", "127.0.0.1:19081", false},
		{"PORT", "127.0.0.1:19081", false, "19123", "127.0.0.1:19123", false},
		{"显式优先", "127.0.0.1:19234", true, "19123", "127.0.0.1:19234", false},
		{"禁用端口", "127.0.0.1:19081", false, "8080", "", true},
		{"低位端口", "127.0.0.1:80", true, "", "", true},
		{"非法 PORT", "127.0.0.1:19081", false, "abc", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveAddress(tt.flag, tt.explicit, tt.port)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}
