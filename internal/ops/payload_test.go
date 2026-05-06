package ops

import (
	"encoding/json"
	"testing"
)

// TestPayload_JSONTagsStable locks the on-wire JSON shape so a careless
// rename doesn't silently break external producers of the canonical
// event format.
func TestPayload_JSONTagsStable(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{
			"FileRead",
			FileReadPayload{Path: "/a", Pattern: "*.go", Lines: 10, Bytes: 120, ContentPreview: "abc"},
			`{"path":"/a","pattern":"*.go","lines":10,"bytes":120,"content_preview":"abc"}`,
		},
		{
			"FileWrite",
			FileWritePayload{Path: "/a", OldString: "o", NewString: "n", ContentPreview: "c", Bytes: 42, Created: true},
			`{"path":"/a","old_string":"o","new_string":"n","content_preview":"c","bytes":42,"created":true}`,
		},
		{
			"FileDelete",
			FileDeletePayload{Path: "/gone"},
			`{"path":"/gone"}`,
		},
		{
			"CommandExec",
			CommandExecPayload{Command: "ls", Cwd: "/tmp", ExitCode: 1, Stdout: "a", Stderr: "b", Duration: 5},
			`{"command":"ls","cwd":"/tmp","exit_code":1,"stdout":"a","stderr":"b","duration_ms":5}`,
		},
		{
			"SkillInvoke",
			SkillInvokePayload{Origin: "qvr-install", Ref: "v1", Reason: "user-request"},
			`{"origin":"qvr-install","ref":"v1","reason":"user-request"}`,
		},
		{
			"Session",
			SessionPayload{ProjectName: "p", Reason: "r"},
			`{"project_name":"p","reason":"r"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("wire shape drift:\n  got:  %s\n  want: %s", got, tc.want)
			}
		})
	}
}

func TestPayload_OmitEmptyStripsZeroFields(t *testing.T) {
	data, _ := json.Marshal(FileWritePayload{Path: "/a"})
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	// Only Path should survive; Bytes, Created false, NewString "" all
	// stripped by omitempty.
	if len(got) != 1 {
		t.Errorf("expected single key; got %v", got)
	}
	if got["path"] != "/a" {
		t.Errorf("expected path=/a; got %v", got["path"])
	}
}
