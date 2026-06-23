package recipe

import "testing"

func TestExecContext_WrappedCommand(t *testing.T) {
	tests := []struct {
		name      string
		ctx       ExecContext
		cmd       string
		want      string
		wantOneOf []string
	}{
		{
			name: "no context",
			ctx:  ExecContext{},
			cmd:  "apt install -y curl",
			want: "apt install -y curl",
		},
		{
			name: "workdir only",
			ctx:  ExecContext{WorkDir: "/app"},
			cmd:  "npm install",
			want: "cd '/app' && npm install",
		},
		{
			name: "env only",
			ctx:  ExecContext{EnvVars: map[string]string{"PORT": "8080"}},
			cmd:  "node server.js",
			want: "export PORT='8080' && node server.js",
		},
		{
			name: "workdir with space",
			ctx:  ExecContext{WorkDir: "/my project"},
			cmd:  "make build",
			want: "cd '/my project' && make build",
		},
		{
			name: "command with single quotes",
			ctx:  ExecContext{WorkDir: "/app"},
			cmd:  "echo 'hello'",
			want: "cd '/app' && echo 'hello'",
		},
		{
			name: "env value with single quotes",
			ctx:  ExecContext{EnvVars: map[string]string{"MSG": "it's fine"}},
			cmd:  "echo $MSG",
			want: "export MSG='it'\\''s fine' && echo $MSG",
		},
		{
			name: "env expansion with pre-expanded PATH",
			ctx: ExecContext{
				EnvVars: map[string]string{"PATH": "/usr/bin", "FOO": "/opt/venv/bin:/usr/bin"},
			},
			cmd:  "make build",
			want: "export FOO='/opt/venv/bin:/usr/bin' PATH='/usr/bin' && make build",
		},
		{
			name: "non-root user wraps with su login shell",
			ctx:  ExecContext{User: "wrenn-user"},
			cmd:  "whoami",
			want: "su 'wrenn-user' -c 'whoami'",
		},
		{
			name: "non-root user with workdir and env",
			ctx:  ExecContext{User: "wrenn-user", WorkDir: "/app", EnvVars: map[string]string{"PORT": "8080"}},
			cmd:  "node server.js",
			want: "su 'wrenn-user' -c 'cd '\\''/app'\\'' && export PORT='\\''8080'\\'' && node server.js'",
		},
		{
			name: "root user is not su-wrapped",
			ctx:  ExecContext{User: "root", WorkDir: "/app"},
			cmd:  "ls",
			want: "cd '/app' && ls",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.ctx.WrappedCommand(tc.cmd)
			if len(tc.wantOneOf) > 0 {
				matched := false
				for _, w := range tc.wantOneOf {
					if got == w {
						matched = true
						break
					}
				}
				if !matched {
					t.Errorf("WrappedCommand(%q)\n  got  %q\n  want one of %q", tc.cmd, got, tc.wantOneOf)
				}
			} else if got != tc.want {
				t.Errorf("WrappedCommand(%q)\n  got  %q\n  want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestExecContext_StartCommand(t *testing.T) {
	tests := []struct {
		name string
		ctx  ExecContext
		cmd  string
		want string
	}{
		{
			name: "no context",
			ctx:  ExecContext{},
			cmd:  "python3 app.py",
			want: `nohup "${SHELL:-/bin/sh}" -c 'python3 app.py' >/dev/null 2>&1 &`,
		},
		{
			name: "with workdir",
			ctx:  ExecContext{WorkDir: "/app"},
			cmd:  "python3 server.py",
			want: `cd '/app' && nohup "${SHELL:-/bin/sh}" -c 'python3 server.py' >/dev/null 2>&1 &`,
		},
		{
			name: "with env",
			ctx:  ExecContext{EnvVars: map[string]string{"PORT": "9000"}},
			cmd:  "node index.js",
			want: `export PORT='9000' && nohup "${SHELL:-/bin/sh}" -c 'node index.js' >/dev/null 2>&1 &`,
		},
		{
			name: "non-root user wraps start with su",
			ctx:  ExecContext{User: "wrenn-user"},
			cmd:  "python3 app.py",
			want: `su 'wrenn-user' -c 'nohup "${SHELL:-/bin/sh}" -c '\''python3 app.py'\'' >/dev/null 2>&1 &'`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.ctx.StartCommand(tc.cmd)
			if got != tc.want {
				t.Errorf("StartCommand(%q)\n  got  %q\n  want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

func TestExpandEnv(t *testing.T) {
	tests := []struct {
		s    string
		vars map[string]string
		want string
	}{
		{
			s:    "hello",
			vars: nil,
			want: "hello",
		},
		{
			s:    "$PATH",
			vars: map[string]string{"PATH": "/usr/bin"},
			want: "/usr/bin",
		},
		{
			s:    "${PATH}",
			vars: map[string]string{"PATH": "/usr/bin"},
			want: "/usr/bin",
		},
		{
			s:    "/opt/venv/bin:$PATH",
			vars: map[string]string{"PATH": "/usr/bin"},
			want: "/opt/venv/bin:/usr/bin",
		},
		{
			s:    "${HOME}/code",
			vars: map[string]string{"HOME": "/root"},
			want: "/root/code",
		},
		{
			s:    "hello $USER",
			vars: map[string]string{"USER": "admin"},
			want: "hello admin",
		},
		{
			s:    "$UNSET",
			vars: map[string]string{"PATH": "/usr/bin"},
			want: "",
		},
		{
			s:    "${UNSET}",
			vars: map[string]string{"PATH": "/usr/bin"},
			want: "",
		},
		{
			s:    "$$",
			vars: map[string]string{"PATH": "/usr/bin"},
			want: "$",
		},
		{
			s:    "price is $$100",
			vars: nil,
			want: "price is $100",
		},
		{
			s:    "$FOO:$BAR",
			vars: map[string]string{"FOO": "a", "BAR": "b"},
			want: "a:b",
		},
		{
			s:    "${FOO}_${BAR}",
			vars: map[string]string{"FOO": "hello", "BAR": "world"},
			want: "hello_world",
		},
		{
			s:    "no vars here",
			vars: nil,
			want: "no vars here",
		},
		{
			s:    "$",
			vars: nil,
			want: "$",
		},
		{
			s:    "${",
			vars: nil,
			want: "${",
		},
		{
			s:    "${}",
			vars: nil,
			want: "",
		},
		{
			s:    "$VAR1$VAR2",
			vars: map[string]string{"VAR1": "a", "VAR2": "b"},
			want: "ab",
		},
	}

	for _, tc := range tests {
		t.Run(tc.s, func(t *testing.T) {
			got := expandEnv(tc.s, tc.vars)
			if got != tc.want {
				t.Errorf("expandEnv(%q, %v)\n  got  %q\n  want %q", tc.s, tc.vars, got, tc.want)
			}
		})
	}
}

func TestShellescape(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "'simple'"},
		{"/path/to/dir", "'/path/to/dir'"},
		{"it's fine", "'it'\\''s fine'"},
		{"", "''"},
		{"a'b'c", "'a'\\''b'\\''c'"},
	}
	for _, tc := range tests {
		got := shellescape(tc.input)
		if got != tc.want {
			t.Errorf("shellescape(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
