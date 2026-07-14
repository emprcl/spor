package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/emprcl/spor/internal/core"
)

func TestRenderVerifyClean(t *testing.T) {
	var buf bytes.Buffer
	renderVerify(&buf, core.VerifyResult{StatesChecked: 3, BlobsChecked: 5})
	out := buf.String()
	if !strings.Contains(out, "intact") {
		t.Errorf("clean output missing 'intact': %q", out)
	}
	if !strings.Contains(out, "3 snapshots") || !strings.Contains(out, "5 blobs") {
		t.Errorf("clean output missing counts: %q", out)
	}
}

func TestRenderVerifyIssues(t *testing.T) {
	var buf bytes.Buffer
	renderVerify(&buf, core.VerifyResult{
		StatesChecked: 1, BlobsChecked: 1,
		Issues: []core.VerifyIssue{
			{Kind: "missing-blob", State: "01ARZ7XYZ0000000000000000", Path: "a.txt", Detail: "blob 01ab is missing"},
		},
	})
	out := buf.String()
	if !strings.Contains(out, "blob 01ab is missing") {
		t.Errorf("issue detail missing: %q", out)
	}
	if !strings.Contains(out, "a.txt") {
		t.Errorf("issue path missing: %q", out)
	}
}
