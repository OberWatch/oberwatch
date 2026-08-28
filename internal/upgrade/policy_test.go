package upgrade

import "testing"

func TestIsUpgrade(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{name: "newer patch is offered", current: "v0.1.3", latest: "v0.1.4", want: true},
		{name: "newer minor is offered", current: "v0.1.9", latest: "v0.2.0", want: true},
		{name: "newer major is offered", current: "v0.9.9", latest: "v1.0.0", want: true},
		{name: "double digit patch is offered", current: "v0.1.9", latest: "v0.1.10", want: true},
		{name: "stable release of the running prerelease is offered", current: "v0.1.4-rc.1", latest: "v0.1.4", want: true},

		{name: "the same version is not offered", current: "v0.1.4", latest: "v0.1.4"},
		{name: "an older patch is not offered", current: "v0.1.4", latest: "v0.1.3"},
		{name: "an older minor is not offered", current: "v0.2.0", latest: "v0.1.9"},
		{name: "an older major is not offered", current: "v1.0.0", latest: "v0.9.9"},
		{name: "a prerelease is never offered", current: "v0.1.3", latest: "v0.1.4-rc.1"},
		{name: "a prerelease is not offered to a lower prerelease either", current: "v0.1.4-rc.1", latest: "v0.1.4-rc.2"},
		{name: "a prerelease of a much newer version is still not offered", current: "v0.1.3", latest: "v9.9.9-rc.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			current := mustParseVersion(t, tt.current)
			latest := mustParseVersion(t, tt.latest)

			if got := IsUpgrade(current, latest); got != tt.want {
				t.Fatalf("IsUpgrade(%s, %s) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}
