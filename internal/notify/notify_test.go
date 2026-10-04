package notify

import "testing"

// TestQuotingCannotEscape: file names reach the notifier. A name with quotes must
// stay inside its string literal rather than become script.
func TestQuotingCannotEscape(t *testing.T) {
	if got := appleString(`a "quoted" \ name`); got != `"a \"quoted\" \\ name"` {
		t.Errorf("appleString = %s", got)
	}
	if got := psString(`it's done'; Remove-Item C:\`); got != `'it''s done''; Remove-Item C:\'` {
		t.Errorf("psString = %s", got)
	}
}
