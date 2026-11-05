package main

import "testing"

func TestDisplayURL(t *testing.T) {
	tests := []struct{ scheme, addr, want string }{
		{"http", ":6565", "http://<IP ของเครื่องนี้>:6565 (รับทุกการ์ดเครือข่าย)"},
		{"https", ":6565", "https://<IP ของเครื่องนี้>:6565 (รับทุกการ์ดเครือข่าย)"},
		{"http", "0.0.0.0:6565", "http://<IP ของเครื่องนี้>:6565 (รับทุกการ์ดเครือข่าย)"},
		{"https", "0.0.0.0:6565", "https://<IP ของเครื่องนี้>:6565 (รับทุกการ์ดเครือข่าย)"},
		{"http", "[::]:6565", "http://<IP ของเครื่องนี้>:6565 (รับทุกการ์ดเครือข่าย)"},
		{"https", "[::]:6565", "https://<IP ของเครื่องนี้>:6565 (รับทุกการ์ดเครือข่าย)"},
		{"http", "127.0.0.1:16565", "http://127.0.0.1:16565"},
		{"https", "127.0.0.1:16565", "https://127.0.0.1:16565"},
		{"http", "192.168.1.5:8080", "http://192.168.1.5:8080"},
		{"https", "192.168.1.5:8080", "https://192.168.1.5:8080"},
		{"http", "[::1]:80", "http://[::1]:80"},
		{"http", "bad", "http://bad"},
	}
	for _, c := range tests {
		if got := displayURL(c.scheme, c.addr); got != c.want {
			t.Errorf("displayURL(%q,%q)=%q want %q", c.scheme, c.addr, got, c.want)
		}
	}
}
