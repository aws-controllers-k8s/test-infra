package issueregion

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRegion(t *testing.T) {
	region := Begin + "\nbody\n" + End
	for name, tc := range map[string]struct {
		body string
		want string
		ok   bool
	}{
		"unique":          {"note\n" + region + "\nafter\n", region, true},
		"crlf":            {Begin + " \r\nbody\r\n" + End + "\r\n", Begin + " \r\nbody\r\n" + End, true},
		"absent":          {"just prose\n", "", false},
		"two begins":      {region + "\n" + Begin + "\n", "", false},
		"reversed":        {End + "\n" + Begin + "\n", "", false},
		"begin inline":    {"see " + Begin + "\nx\n" + End + "\n", "", false},
		"end not on line": {Begin + "\nx " + End + "\n", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := Region(tc.body)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFindShapes(t *testing.T) {
	_, _, s := Find("")
	assert.Equal(t, Absent, s)
	_, _, s = Find(Begin + "\n")
	assert.Equal(t, Malformed, s)
	start, end, s := Find("x\n" + Begin + "\n" + End + "\n")
	assert.Equal(t, Unique, s)
	assert.Equal(t, 2, start)
	assert.Equal(t, 2+len(Begin)+1+len(End), end)
}
