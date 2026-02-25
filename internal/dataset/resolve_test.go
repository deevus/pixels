package dataset

import "testing"

func TestResolve(t *testing.T) {
	tests := []struct {
		name           string
		pool           string
		container      string
		overridePrefix string
		want           string
	}{
		{
			name:      "default convention",
			pool:      "tank",
			container: "px-my-project",
			want:      "tank/incus/containers/px-my-project",
		},
		{
			name:      "different pool",
			pool:      "ssd",
			container: "px-sandbox",
			want:      "ssd/incus/containers/px-sandbox",
		},
		{
			name:           "override prefix",
			pool:           "tank",
			container:      "px-my-project",
			overridePrefix: "tank/custom/path",
			want:           "tank/custom/path/px-my-project",
		},
		{
			name:           "override ignores pool",
			pool:           "ignored",
			container:      "px-test",
			overridePrefix: "ssd/incus/containers",
			want:           "ssd/incus/containers/px-test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.pool, tt.container, tt.overridePrefix)
			if got != tt.want {
				t.Errorf("Resolve(%q, %q, %q) = %q, want %q",
					tt.pool, tt.container, tt.overridePrefix, got, tt.want)
			}
		})
	}
}
