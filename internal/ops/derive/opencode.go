package derive

import (
	"encoding/json"
	"strings"

	"github.com/astra-sh/qvr/internal/ops"
)

func init() { Register("opencode", opencodeDeriver{}) }

// opencodeDeriver reconstructs the Turn→Tool/Skill hierarchy from an OpenCode
// session captured out of opencode.db by rawtrace.IngestOpencodeDB (schema
// observed live, 2026-06-11): one "type":"session" header row (directory,
// model JSON, token totals), then "type":"message" rows ({role, model}) and
// "type":"part" rows ({type: text|reasoning|tool, tool, state:{input,
// output}}), all in creation order (time-prefixed ids).
//
// OpenCode's skill mechanism is a first-class tool literally named "skill"
// with input {"name":"<skill>"} (covered by ops.SkillRefFromTool) whose
// state.metadata.dir records the loaded skill's directory — the load-path
// evidence that pins which artifact ran (observed live, 2026-06-11);
// supporting-file reads/execs carry full paths for the shared path signal.
type opencodeDeriver struct{}

// opencodeRow is the capture envelope (session header, message, or part).
type opencodeRow struct {
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	MessageID   string          `json:"message_id"`
	Model       string          `json:"model"` // session header: model JSON string
	Data        json.RawMessage `json:"data"`
	TimeCreated int64           `json:"time_created"` // epoch ms
}

// opencodeMessageData is the message row's data JSON.
type opencodeMessageData struct {
	Role  string `json:"role"`
	Model *struct {
		ProviderID string `json:"providerID"`
		ModelID    string `json:"modelID"`
	} `json:"model"`
}

// opencodePartData is the part row's data JSON.
type opencodePartData struct {
	Type   string `json:"type"` // text | reasoning | tool | …
	Text   string `json:"text"`
	Tool   string `json:"tool"`
	CallID string `json:"callID"`
	State  *struct {
		Input    map[string]any `json:"input"`
		Output   string         `json:"output"`
		Metadata *struct {
			Dir string `json:"dir"` // skill tool: the loaded skill's directory
		} `json:"metadata"`
	} `json:"state"`
}

func (opencodeDeriver) Derive(rows []*ops.RawTrace) (*Derivation, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	w := &turnWalk{sessionID: rows[0].SessionID.String()}
	out := &Derivation{}
	roles := map[string]string{} // message id → role

	for _, r := range rows {
		if r.Source != ops.RawSourceTranscript {
			continue
		}
		var row opencodeRow
		if err := json.Unmarshal(r.Raw, &row); err != nil {
			continue
		}
		switch row.Type {
		case "session":
			w.setModel(opencodeModel(row.Model))
		case "message":
			var d opencodeMessageData
			if err := json.Unmarshal(row.Data, &d); err == nil {
				roles[row.ID] = d.Role
				if d.Model != nil && d.Model.ModelID != "" {
					w.setModel(strings.TrimPrefix(d.Model.ProviderID+"/"+d.Model.ModelID, "/"))
				}
			}
		case "part":
			var d opencodePartData
			if err := json.Unmarshal(row.Data, &d); err != nil {
				continue
			}
			opencodePart(w, &d, roles[row.MessageID], row.TimeCreated)
		}
	}
	w.flush()
	out.Spans = w.spans
	return out, nil
}

// opencodePart folds one part into the walk.
func opencodePart(w *turnWalk, d *opencodePartData, role string, ts int64) {
	switch d.Type {
	case "text":
		switch normType(role) {
		case "user":
			prompt := strings.Trim(stripSystemReminder(d.Text), `"`)
			if prompt == "" {
				return
			}
			w.open(ts)
			w.cur.prompt = prompt
		case "assistant":
			w.ensure(ts)
			if d.Text != "" {
				w.cur.appendOutput(d.Text)
			}
			w.cur.bump(ts)
		}
	case "tool":
		w.ensure(ts)
		var input map[string]any
		var output string
		if d.State != nil {
			input = d.State.Input
			output = d.State.Output
		}
		argsJSON := compactJSON(input)
		cmdText := commandFromArgs(input)
		ref := resolveSkillRef(d.Tool, input, cmdText, argsJSON, nil)
		// The skill tool's state.metadata.dir is the loaded skill's actual
		// directory — the load-path evidence a name-only invocation lacks.
		if ref.name != "" && ref.loadPath == "" && d.State != nil && d.State.Metadata != nil {
			ref.loadPath = d.State.Metadata.Dir
		}
		w.cur.addToolInvocation(d.Tool, d.CallID, argsJSON, cmdText, ref, ts, w.sessionID)
		if output != "" {
			w.cur.applyResultLast(output, ts, false)
		}
		w.cur.bump(ts)
	}
}

// opencodeModel renders the session header's model JSON ({"id","providerID"})
// as provider/id.
func opencodeModel(raw string) string {
	if raw == "" {
		return ""
	}
	var m struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerID"`
		ModelID    string `json:"modelID"`
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return ""
	}
	id := firstNonEmptyStr(m.ModelID, m.ID)
	if id == "" {
		return ""
	}
	if m.ProviderID != "" {
		return m.ProviderID + "/" + id
	}
	return id
}
