package claude

import "testing"

func TestParse(t *testing.T) {
	r, err := parse([]byte(`{"type":"result","subtype":"success","result":"done","session_id":"s1","num_turns":4,"total_cost_usd":0.5,"modelUsage":{"m2":{},"m1":{}}}`))
	if err != nil || r.Text != "done" || r.SessionID != "s1" || r.Turns != 4 || r.CostUSD != 0.5 || r.Model != "m1,m2" {
		t.Fatalf("%+v %v", r, err)
	}
	r, err = parse([]byte(`{"type":"result","subtype":"error_max_turns","is_error":true,"result":null,"num_turns":10}`))
	if err != nil || r.Subtype != "error_max_turns" || r.Text != "" {
		t.Fatalf("max turns: %+v %v", r, err)
	}
	if _, err := parse([]byte("panic: boom")); err == nil {
		t.Fatal("junk parsed")
	}
}
