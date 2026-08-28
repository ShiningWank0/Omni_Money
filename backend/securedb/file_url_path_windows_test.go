package securedb

import (
	"net/url"
	"testing"
)

func TestDatabaseFileURLUsesWindowsFileURIPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "drive absolute",
			path: `D:\a\_temp\Omni Money\probe.db`,
			want: "file:///D:/a/_temp/Omni%20Money/probe.db?mode=ro",
		},
		{
			name: "drive relative remains relative",
			path: `D:vaults\probe.db`,
			want: "file:D:vaults/probe.db?mode=ro",
		},
		{
			name: "UNC path has empty URI authority",
			path: `\\server\share\probe.db`,
			want: "file:////server/share/probe.db?mode=ro",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			query := url.Values{"mode": []string{"ro"}}
			if got := databaseFileURL(test.path, query); got != test.want {
				t.Fatalf("databaseFileURL() = %q, want %q", got, test.want)
			}
		})
	}
}
