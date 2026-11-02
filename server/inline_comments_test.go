package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestInlineCommentSurvivesPageReplacement(t *testing.T) {
	s, tab, _ := newEditorTab(t)
	anchor := `{"gen":0,"quote":"selected text","from":{"item":{"client":1,"clock":1}},"to":{"item":{"client":1,"clock":2}}}`
	send := func(cookie, payload string) int {
		req, _ := http.NewRequest("POST", tab.srv.URL+"/api/pages/"+tab.page+"/comments", strings.NewReader(payload))
		req.Header.Set("Cookie", cookie)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	if code := send(tab.cookie, `{"body":"feedback","anchor":`+anchor+`}`); code != http.StatusOK {
		t.Fatalf("create: %d", code)
	}
	if code := send("", `{"body":"forbidden","anchor":`+anchor+`}`); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", code)
	}
	if code, _ := tab.patch("", "replacement"); code != http.StatusOK {
		t.Fatalf("replace: %d", code)
	}
	if code := send(tab.cookie, `{"body":"stale","anchor":`+anchor+`}`); code != http.StatusConflict {
		t.Fatalf("stale anchor: %d", code)
	}
	if code := send(tab.cookie, `{"body":"page comment"}`); code != http.StatusOK {
		t.Fatalf("page comment: %d", code)
	}
	comments, err := s.pageComments(tab.page)
	if err != nil || len(comments) != 2 {
		t.Fatalf("comments after replacement: %v %v", comments, err)
	}
	var saved struct {
		Gen   int    `json:"gen"`
		Quote string `json:"quote"`
	}
	if err := json.Unmarshal(comments[0].Anchor, &saved); err != nil || saved.Gen != 0 || saved.Quote != "selected text" {
		t.Fatalf("anchor: %s %v", comments[0].Anchor, err)
	}
	if len(comments[1].Anchor) != 0 {
		t.Fatalf("page comment has an anchor: %s", comments[1].Anchor)
	}
}
