package findingsblock

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var hdr = Header{Service: "s3", From: "v1.41.5", To: "service/s3/v1.97.3"}

func TestFormatParseRoundTrip(t *testing.T) {
	lines := []Line{
		{Class: "spec-field", Kind: "Bucket", Subject: "ABAC", Bucket: BucketRegen},
		{Class: "spec-field", Kind: "Bucket", Subject: "Mode", Bucket: BucketNeedsConfig, Work: "create-only"},
		{Class: "new-resource", Kind: "Table", Subject: "Table", Bucket: BucketNewResource},
	}
	out, omitted := Format(hdr, lines, 1<<16)
	assert.Equal(t, 0, omitted)
	assert.Equal(t,
		`<!-- ack-api-change-findings v1 service="s3" from="v1.41.5" to="service/s3/v1.97.3" count=3 omitted=0`+"\n"+
			`"new-resource"|"Table"|"Table"|"new-resource"|""`+"\n"+
			`"spec-field"|"Bucket"|"Mode"|"needs-config"|"create-only"`+"\n"+
			`"spec-field"|"Bucket"|"ABAC"|"regen"|""`+"\n"+
			"-->\n",
		out)

	b, found, err := Parse("intro\n" + out + "footer\n")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, hdr, b.Header)
	assert.Equal(t, 3, b.Count)
	assert.Equal(t, 0, b.Omitted)
	assert.Equal(t, []Line{lines[2], lines[1], lines[0]}, b.Lines)
}

func TestFormatNeverClosesTheCommentEarly(t *testing.T) {
	lines := []Line{{Class: "spec-field", Kind: "K", Subject: "a-->b---c", Bucket: BucketRegen}}
	out, _ := Format(hdr, lines, 1<<16)
	inner := strings.TrimSuffix(strings.TrimPrefix(out, Begin), End+"\n")
	assert.NotContains(t, inner, "--")

	b, _, err := Parse(out)
	require.NoError(t, err)
	assert.Equal(t, "a-->b---c", b.Lines[0].Subject)
}

func TestFormatKeepsWorkFirstWhenTruncating(t *testing.T) {
	lines := []Line{
		{Class: "spec-field", Kind: "Bucket", Subject: "ABAC", Bucket: BucketRegen},
		{Class: "spec-field", Kind: "Bucket", Subject: "Mode", Bucket: BucketNeedsConfig, Work: "create-only"},
	}
	full, _ := Format(hdr, lines, 1<<16)

	out, omitted := Format(hdr, lines, len(full)-1)
	assert.Equal(t, 1, omitted)
	assert.NotContains(t, out, `"ABAC"`)

	b, _, err := Parse(out)
	require.NoError(t, err)
	assert.Equal(t, 1, b.Count)
	assert.Equal(t, 1, b.Omitted)
	assert.Equal(t, "Mode", b.Lines[0].Subject)
}

func TestFormatReturnsEmptyWhenHeaderDoesNotFit(t *testing.T) {
	out, omitted := Format(hdr, []Line{{Class: "x", Bucket: BucketRegen}}, 10)
	assert.Equal(t, "", out)
	assert.Equal(t, 1, omitted)
}

func TestParseAbsent(t *testing.T) {
	_, found, err := Parse("no block here")
	assert.NoError(t, err)
	assert.False(t, found)
}

func TestParseRejects(t *testing.T) {
	good, _ := Format(hdr, []Line{{Class: "spec-field", Kind: "B", Subject: "S", Bucket: BucketRegen}}, 1<<16)
	for name, body := range map[string]string{
		"two blocks":      good + good,
		"count mismatch":  strings.Replace(good, "count=1", "count=2", 1),
		"unknown version": strings.Replace(good, Begin+"v1 ", Begin+"v2 ", 1),
		"unterminated":    strings.TrimSuffix(good, End+"\n"),
		"bad line":        strings.Replace(good, `"regen"|""`, `"regen"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, found, err := Parse(body)
			assert.True(t, found)
			assert.Error(t, err)
		})
	}
}

func TestParseAcceptsCRLF(t *testing.T) {
	good, _ := Format(hdr, []Line{{Class: "spec-field", Kind: "B", Subject: "S", Bucket: BucketRegen}}, 1<<16)
	b, _, err := Parse(strings.ReplaceAll(good, "\n", "\r\n"))
	require.NoError(t, err)
	assert.Len(t, b.Lines, 1)
}
