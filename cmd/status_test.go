package cmd

import "testing"

func TestParseZmxList(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []zmxSession
	}{
		{
			name: "empty",
			raw:  "",
			want: nil,
		},
		{
			name: "single completed session",
			raw:  "session_name=px-egress\tpid=1234\tclients=0\tcreated_at=1772329252\ttask_ended_at=1772329255\ttask_exit_code=0\tstarted_in=/root\tcmd=bash -c 'apt-get install nftables'",
			want: []zmxSession{
				{Name: "px-egress", PID: "1234", EndedAt: "1772329255", ExitCode: "0", Cmd: "bash -c 'apt-get install nftables'"},
			},
		},
		{
			name: "running session no ended_at",
			raw:  "session_name=px-devtools\tpid=5678\tclients=0\tcreated_at=1772329260\tstarted_in=/root\tcmd=/usr/local/bin/pixels-setup-devtools.sh",
			want: []zmxSession{
				{Name: "px-devtools", PID: "5678", EndedAt: "", ExitCode: "", Cmd: "/usr/local/bin/pixels-setup-devtools.sh"},
			},
		},
		{
			name: "multiple sessions",
			raw: "session_name=px-egress\tpid=1234\tclients=0\tcreated_at=1772329252\ttask_ended_at=1772329255\ttask_exit_code=0\tstarted_in=/root\tcmd=bash\n" +
				"session_name=px-devtools\tpid=5678\tclients=0\tcreated_at=1772329260\ttask_ended_at=1772329400\ttask_exit_code=1\tstarted_in=/root\tcmd=bash",
			want: []zmxSession{
				{Name: "px-egress", PID: "1234", EndedAt: "1772329255", ExitCode: "0", Cmd: "bash"},
				{Name: "px-devtools", PID: "5678", EndedAt: "1772329400", ExitCode: "1", Cmd: "bash"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseZmxList(tt.raw)
			if len(got) != len(tt.want) {
				t.Fatalf("parseZmxList() returned %d sessions, want %d", len(got), len(tt.want))
			}
			for i, w := range tt.want {
				g := got[i]
				if g.Name != w.Name || g.PID != w.PID || g.EndedAt != w.EndedAt || g.ExitCode != w.ExitCode || g.Cmd != w.Cmd {
					t.Errorf("session[%d] = %+v, want %+v", i, g, w)
				}
			}
		})
	}
}
