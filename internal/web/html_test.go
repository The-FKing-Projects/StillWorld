package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/The-FKing-Projects/StillWorld/action"
	"github.com/The-FKing-Projects/StillWorld/internal/actionengine"
	"github.com/The-FKing-Projects/StillWorld/presentation"
)

type testPresentation struct{}

func (testPresentation) Home(context.Context, action.Player) (presentation.Page, error) {
	return presentation.Page{Title: "<Game>", Forms: []presentation.Form{{
		Action: "test.run", Label: "Run",
		Numbers: []presentation.NumberField{{Name: "amount", Label: "Amount", Value: 1, Min: 1, Max: 100}},
	}}}, nil
}

func (testPresentation) DecodeForm(_ string, fields url.Values) (json.RawMessage, error) {
	return json.Marshal(fields)
}

func (testPresentation) Result(_ string, _ json.RawMessage) (string, error) {
	return "<result>", nil
}

func TestHTMLUncertainOutcomeKeepsOriginalForm(t *testing.T) {
	authenticator := newFakeAuth()
	authenticator.sessions["session-token"] = "player"
	actions := &fakeActionService{executeErr: actionengine.ErrOutcomeUnknown}
	handler := New(authenticator, Options{Actions: actions, Presentation: testPresentation{}}).Handler()
	cookie := &http.Cookie{Name: "__Host-session", Value: "session-token"}
	form := url.Values{"request_id": {"original-token"}, "value": {"<unsafe>"}}
	response := post(handler, "/actions/test.run", form, func(r *http.Request) { r.AddCookie(cookie) })
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	body := response.Body.String()
	for _, expected := range []string{"may have completed", "original-token", `value="&lt;unsafe&gt;"`, "Retry the original action"} {
		if !strings.Contains(body, expected) {
			t.Errorf("missing %q in %s", expected, body)
		}
	}
	if strings.Contains(body, `value="<unsafe>"`) || actions.issueCalls != 0 ||
		actions.executedToken != "original-token" {
		t.Fatalf("unsafe or replaced retry: body=%s action=%+v", body, actions)
	}
}

func TestHTMLHomeEscapesGameContent(t *testing.T) {
	authenticator := newFakeAuth()
	authenticator.sessions["session-token"] = "player"
	actions := &fakeActionService{issueRequestID: actionengine.RequestID{Token: "token"}}
	handler := New(authenticator, Options{Actions: actions, Presentation: testPresentation{}}).Handler()
	response := get(handler, "/", &http.Cookie{Name: "__Host-session", Value: "session-token"})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "&lt;Game&gt;") ||
		strings.Contains(response.Body.String(), "<Game>") {
		t.Fatalf("home = %d %s", response.Code, response.Body)
	}
	for _, expected := range []string{`<label for="test.run-amount">Amount</label>`, `name="amount" type="number" value="1" min="1" max="100" step="1" required`} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Errorf("missing numeric input %q: %s", expected, response.Body)
		}
	}
}

func TestHTMLRejectsDuplicateInputFields(t *testing.T) {
	authenticator := newFakeAuth()
	authenticator.sessions["session-token"] = "player"
	actions := &fakeActionService{}
	handler := New(authenticator, Options{Actions: actions, Presentation: testPresentation{}}).Handler()
	form := url.Values{"request_id": {"token"}, "amount": {"1", "2"}}
	response := post(handler, "/actions/test.run", form, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "__Host-session", Value: "session-token"})
	})
	if response.Code != http.StatusBadRequest || actions.executeCalls != 0 {
		t.Fatalf("duplicate numeric input = %d, execute calls = %d: %s", response.Code, actions.executeCalls, response.Body)
	}
}

func TestInvalidNumberFieldsAreRejected(t *testing.T) {
	tests := []presentation.Form{
		{Action: "test.run", Numbers: []presentation.NumberField{{Name: "request_id", Label: "Reserved", Max: 1}}},
		{Action: "test.run", Numbers: []presentation.NumberField{{Name: "amount", Label: "Amount", Min: 2, Max: 1}}},
		{Action: "test.run", Numbers: []presentation.NumberField{{Name: "amount", Label: "Amount", Value: 3, Min: 1, Max: 2}}},
		{Action: "test.run", Numbers: []presentation.NumberField{{Name: "amount", Label: "A", Max: 1}, {Name: "amount", Label: "B", Max: 1}}},
	}
	for _, form := range tests {
		if err := validatePageForms([]presentation.Form{form}); err == nil {
			t.Errorf("accepted invalid form %+v", form)
		}
	}
}
