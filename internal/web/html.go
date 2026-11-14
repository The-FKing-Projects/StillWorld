package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/The-FKing-Projects/StillWorld/action"
	"github.com/The-FKing-Projects/StillWorld/internal/actiondata"
	"github.com/The-FKing-Projects/StillWorld/internal/actionengine"
	"github.com/The-FKing-Projects/StillWorld/internal/auth"
	"github.com/The-FKing-Projects/StillWorld/presentation"
)

type resultService interface {
	Result(ctx context.Context, playerID int64, nonce string) (string, actionengine.Outcome, error)
}

type turnsReader interface {
	CurrentTurns(context.Context, int64) (int64, error)
}

type htmlForm struct {
	presentation.Form
	RequestID string
}

type htmlPage struct {
	Title       string
	Description string
	Username    string
	Turns       *int64
	Sections    []presentation.Section
	Forms       []htmlForm
	AuthKind    string
	Message     string
	Recovery    bool
	NoGame      bool
	ResetDone   bool
	RetryAction string
	RetryFields url.Values
}

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
body{font-family:system-ui,sans-serif;line-height:1.55;background:#eef3f1;color:#142a27;margin:0}
header{background:#173f3a;color:#fff;padding:1rem max(1rem,calc((100% - 60rem)/2))}
header a{color:#fff;font-weight:700} main{max-width:60rem;margin:auto;padding:1.5rem 1rem 4rem}
h1,h2{line-height:1.2} h2{margin-top:2rem} a{color:#075b54} p{max-width:70ch}
label{display:block;font-weight:600;margin-top:1rem} input:not([type=hidden]){display:block;box-sizing:border-box;width:min(100%,24rem);padding:.6rem;font:inherit;border:1px solid #536a64}
button{font:inherit;font-weight:650;background:#17584d;color:white;border:2px solid #17584d;padding:.6rem 1rem;cursor:pointer}
button:hover{background:#0d3d35} :focus-visible{outline:3px solid #d77b00;outline-offset:3px}
.notice{border:1px solid #536a64;padding:1rem;background:#fff}.actions{display:flex;flex-wrap:wrap;gap:1.5rem}.actions form{flex:1 1 15rem}
.logout{display:inline}.logout button{background:transparent;color:#fff;border-color:#fff;margin-left:1rem}
</style></head><body><header>{{if .NoGame}}StillWorld{{else}}<a href="/">Game home</a>{{end}}{{if .Username}} <span>Signed in as {{.Username}}</span>
<form class="logout" method="post" action="/logout"><input type="hidden" name="_html" value="1"><button type="submit">Sign out</button></form>{{end}}</header>
<main><h1>{{.Title}}</h1>{{if .Description}}<p>{{.Description}}</p>{{end}}{{if .Turns}}<p><strong>Available Turns:</strong> {{.Turns}}</p>{{end}}
{{if .Message}}<p class="notice" role="status">{{.Message}}</p>{{end}}
{{if .AuthKind}}<form method="post" action="/{{.AuthKind}}"><input type="hidden" name="_html" value="1">
{{if eq .AuthKind "forgot-password"}}
<label for="email">Email address</label><input id="email" name="email" type="email" autocomplete="email" required maxlength="254">
<p><button type="submit">Send reset code</button></p>
{{else if eq .AuthKind "reset-password"}}
<label for="code">Reset code</label><input id="code" name="code" autocomplete="off" required maxlength="43">
<label for="password">New password</label><input id="password" name="password" type="password" autocomplete="new-password" required minlength="8">
<label for="confirm_password">Confirm new password</label><input id="confirm_password" name="confirm_password" type="password" autocomplete="new-password" required minlength="8">
<p><button type="submit">Change password</button></p>
{{else}}
<label for="username">Username</label><input id="username" name="username" autocomplete="username" required minlength="3" maxlength="32">
{{if eq .AuthKind "signup"}}<label for="email">Email (optional)</label><input id="email" name="email" type="email" autocomplete="email">{{end}}
<label for="password">Password</label><input id="password" name="password" type="password" {{if eq .AuthKind "signup"}}autocomplete="new-password"{{else}}autocomplete="current-password"{{end}} required minlength="8">
<p><button type="submit">{{if eq .AuthKind "signup"}}Create account{{else}}Sign in{{end}}</button></p>{{end}}</form>
{{if eq .AuthKind "signup"}}<p>Already have an account? <a href="/login">Sign in</a>.</p>
{{else if eq .AuthKind "login"}}<p>New here? <a href="/signup">Create an account</a>.</p>{{if .Recovery}}<p><a href="/forgot-password">Forgot your password?</a></p>{{end}}
{{else if eq .AuthKind "forgot-password"}}<p>Already have a code? <a href="/reset-password">Reset your password</a>.</p>{{if not .NoGame}}<p><a href="/login">Return to sign in</a></p>{{end}}
{{else}}<p><a href="/forgot-password">Request a new code</a>{{if not .NoGame}} or <a href="/login">return to sign in</a>{{end}}.</p>{{end}}{{end}}
{{if and .Recovery (not .AuthKind)}}<p>{{if .ResetDone}}<a href="/forgot-password">Request a new code</a>{{else}}<a href="/reset-password">Enter a reset code</a>{{end}}{{if not .NoGame}} or <a href="/login">return to sign in</a>{{end}}.</p>{{end}}
{{range .Sections}}<section><h2>{{.Heading}}</h2><ul>{{range .Lines}}<li>{{.}}</li>{{end}}</ul></section>{{end}}
{{if .Forms}}<section><h2>Actions</h2><div class="actions">{{range .Forms}}{{$action := .Action}}<form method="post" action="/actions/{{.Action}}">
<input type="hidden" name="request_id" value="{{.RequestID}}"><p>{{.Description}}</p>{{range .Numbers}}
<label for="{{$action}}-{{.Name}}">{{.Label}}</label><input id="{{$action}}-{{.Name}}" name="{{.Name}}" type="number" value="{{.Value}}" min="{{.Min}}" max="{{.Max}}" step="1" required>{{end}}
<p><button type="submit">{{.Label}}</button></p></form>{{end}}</div></section>{{end}}
{{if .RetryAction}}<form method="post" action="/actions/{{.RetryAction}}">{{range $name,$values := .RetryFields}}{{range $values}}<input type="hidden" name="{{$name}}" value="{{.}}">{{end}}{{end}}
<button type="submit">Retry the original action</button></form>{{end}}
{{if and (not .AuthKind) (not .Forms) (not .Recovery)}}<p><a href="/">Return to the game</a></p>{{end}}
</main></body></html>`))

var formFieldName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func validatePageForms(forms []presentation.Form) error {
	actions := make(map[string]bool, len(forms))
	for _, form := range forms {
		if actions[form.Action] {
			return fmt.Errorf("duplicate form for action %q", form.Action)
		}
		actions[form.Action] = true
		fields := make(map[string]bool, len(form.Numbers))
		for _, field := range form.Numbers {
			if !formFieldName.MatchString(field.Name) || field.Name == "request_id" ||
				fields[field.Name] || field.Label == "" || field.Min > field.Max ||
				field.Value < field.Min || field.Value > field.Max {
				return fmt.Errorf("invalid number field %q for action %q", field.Name, form.Action)
			}
			fields[field.Name] = true
		}
	}
	return nil
}

func renderHTML(w http.ResponseWriter, status int, page htmlPage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.WriteHeader(status)
	if err := pageTemplate.Execute(w, page); err != nil {
		log.Printf("render HTML page: %v", err)
	}
}

func (s *Server) htmlForm(r *http.Request) bool {
	return s.opts.Presentation != nil && r.PostForm.Get("_html") == "1"
}

func (s *Server) authError(w http.ResponseWriter, r *http.Request, kind, message string, status int) {
	if !s.htmlForm(r) {
		http.Error(w, message, status)
		return
	}
	renderHTML(w, status, htmlPage{Title: authTitle(kind), AuthKind: kind, Message: message, Recovery: s.opts.Recovery != nil})
}

func authTitle(kind string) string {
	if kind == "signup" {
		return "Create an account"
	}
	return "Sign in"
}

func (s *Server) handleHTMLLogin(w http.ResponseWriter, _ *http.Request) {
	renderHTML(w, http.StatusOK, htmlPage{Title: "Sign in", AuthKind: "login", Recovery: s.opts.Recovery != nil})
}

func (s *Server) handleHTMLSignup(w http.ResponseWriter, _ *http.Request) {
	renderHTML(w, http.StatusOK, htmlPage{Title: "Create an account", AuthKind: "signup"})
}

func (s *Server) handleHTMLForgotPassword(w http.ResponseWriter, _ *http.Request) {
	renderHTML(w, http.StatusOK, htmlPage{Title: "Forgot your password?", Description: "Enter the email address on your account. We will send a reset code if the account is eligible.", AuthKind: "forgot-password", Recovery: true, NoGame: s.recoveryNoGame()})
}

func (s *Server) handleHTMLResetPassword(w http.ResponseWriter, _ *http.Request) {
	renderHTML(w, http.StatusOK, htmlPage{Title: "Reset password", Description: "Enter the code from your email and choose a new password.", AuthKind: "reset-password", Recovery: true, NoGame: s.recoveryNoGame()})
}

func (s *Server) htmlAccount(w http.ResponseWriter, r *http.Request) (auth.Account, bool) {
	cookie, err := r.Cookie(s.cookieName)
	if err == nil {
		account, err := s.auth.Authenticate(r.Context(), cookie.Value)
		if err == nil {
			s.setCookie(w, cookie.Value, int(auth.IdleTimeout.Seconds()))
			return account, true
		}
		if !errors.Is(err, auth.ErrNoSession) {
			log.Printf("authenticate HTML request: %v", err)
			renderHTML(w, http.StatusServiceUnavailable, htmlPage{Title: "Temporarily unavailable", Message: "Your session could not be checked. Try again."})
			return auth.Account{}, false
		}
		s.setCookie(w, "", -1)
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
	return auth.Account{}, false
}

func (s *Server) handleHTMLHome(w http.ResponseWriter, r *http.Request) {
	ipSlot, status := s.reserveActionIP(r)
	if status != 0 {
		renderHTML(w, status, htmlPage{Title: "Please wait", Message: "Too many requests. Try again later."})
		return
	}
	account, ok := s.htmlAccount(w, r)
	if !ok {
		s.finishActionIP(ipSlot)
		return
	}
	userSlot := s.actionUser.reserve(strconv.FormatInt(account.ID, 10))
	if userSlot == nil {
		s.finishActionIP(ipSlot)
		renderHTML(w, http.StatusTooManyRequests, htmlPage{Title: "Please wait", Message: "Too many requests. Try again later."})
		return
	}
	page, err := s.opts.Presentation.Home(r.Context(), action.Player{ID: account.ID})
	s.actionUser.finishCounted(userSlot)
	s.finishActionIP(ipSlot)
	if err != nil {
		log.Printf("render game home: %v", err)
		renderHTML(w, http.StatusServiceUnavailable, htmlPage{Title: "Temporarily unavailable", Message: "The game state could not be loaded. Try again."})
		return
	}
	if err := validatePageForms(page.Forms); err != nil {
		log.Printf("invalid HTML game forms: %v", err)
		renderHTML(w, http.StatusServiceUnavailable, htmlPage{Title: "Temporarily unavailable", Message: "The game actions could not be displayed. Try again later."})
		return
	}
	data := htmlPage{Title: page.Title, Description: page.Description, Username: account.Username, Sections: page.Sections}
	if reader, ok := s.actions.(turnsReader); ok {
		ipSlot, status := s.reserveActionIP(r)
		if status != 0 {
			renderHTML(w, status, htmlPage{Title: "Please wait", Message: "Too many requests. Try again later."})
			return
		}
		userSlot := s.actionUser.reserve(strconv.FormatInt(account.ID, 10))
		if userSlot == nil {
			s.finishActionIP(ipSlot)
			renderHTML(w, http.StatusTooManyRequests, htmlPage{Title: "Please wait", Message: "Too many requests. Try again later."})
			return
		}
		balance, err := reader.CurrentTurns(r.Context(), account.ID)
		s.actionUser.finishCounted(userSlot)
		s.finishActionIP(ipSlot)
		if err != nil {
			log.Printf("read HTML player Turns: %v", err)
			renderHTML(w, http.StatusServiceUnavailable, htmlPage{Title: "Temporarily unavailable", Message: "Your Turns could not be loaded. Try again."})
			return
		}
		data.Turns = &balance
	}
	for _, form := range page.Forms {
		ipSlot, status := s.reserveActionIP(r)
		if status != 0 {
			renderHTML(w, status, htmlPage{Title: "Please wait", Message: "Too many requests. Try again later."})
			return
		}
		userSlot := s.actionUser.reserve(strconv.FormatInt(account.ID, 10))
		if userSlot == nil {
			s.finishActionIP(ipSlot)
			renderHTML(w, http.StatusTooManyRequests, htmlPage{Title: "Please wait", Message: "Too many requests. Try again later."})
			return
		}
		token, err := s.actions.IssueRequestID(r.Context(), account.ID, form.Action)
		s.actionUser.finishCounted(userSlot)
		s.finishActionIP(ipSlot)
		if err != nil {
			log.Printf("issue HTML action request: %v", err)
			renderHTML(w, http.StatusServiceUnavailable, htmlPage{Title: "Temporarily unavailable", Message: "Actions could not be prepared. Try again."})
			return
		}
		data.Forms = append(data.Forms, htmlForm{Form: form, RequestID: token.Token})
	}
	renderHTML(w, http.StatusOK, data)
}

func (s *Server) handleHTMLAction(w http.ResponseWriter, r *http.Request) {
	ipSlot, status := s.reserveActionIP(r)
	if status != 0 {
		renderHTML(w, status, htmlPage{Title: "Please wait", Message: "Too many requests. Retry the original form."})
		return
	}
	defer s.finishActionIP(ipSlot)
	account, ok := s.htmlAccount(w, r)
	if !ok {
		return
	}
	userSlot := s.actionUser.reserve(strconv.FormatInt(account.ID, 10))
	if userSlot == nil {
		renderHTML(w, http.StatusTooManyRequests, htmlPage{Title: "Please wait", Message: "Too many requests. Retry the original form."})
		return
	}
	defer s.actionUser.finishCounted(userSlot)
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		renderHTML(w, http.StatusUnsupportedMediaType, htmlPage{Title: "Invalid form", Message: "Submit the action from the game page."})
		return
	}
	if err := r.ParseForm(); err != nil || len(r.PostForm["request_id"]) != 1 || r.PostForm.Get("request_id") == "" {
		renderHTML(w, http.StatusBadRequest, htmlPage{Title: "Invalid form", Message: "The action request is missing or invalid. Return to the game to begin again."})
		return
	}
	fields := make(url.Values, len(r.PostForm))
	for key, values := range r.PostForm {
		if key != "request_id" && len(values) != 1 {
			renderHTML(w, http.StatusBadRequest, htmlPage{Title: "Invalid form", Message: "The submitted action has duplicate fields. Return to the game to begin again."})
			return
		}
		fields[key] = append([]string(nil), values...)
	}
	fields.Del("request_id")
	name := r.PathValue("actionName")
	input, err := s.opts.Presentation.DecodeForm(name, fields)
	if err != nil {
		renderHTML(w, http.StatusBadRequest, htmlPage{Title: "Invalid form", Message: "The submitted action is invalid. Return to the game to begin again."})
		return
	}
	if err := actiondata.ValidateInput(input); err != nil {
		log.Printf("HTML form adapter returned invalid action input: %v", err)
		renderHTML(w, http.StatusInternalServerError, htmlPage{Title: "Action unavailable", Message: "This action could not be prepared. Please try again later."})
		return
	}
	outcome, err := s.actions.Execute(r.Context(), account.ID, name, r.PostForm.Get("request_id"), input)
	if err == nil {
		if len(outcome.Nonce) != 32 {
			log.Printf("HTML action returned invalid result nonce")
			renderHTML(w, http.StatusServiceUnavailable, htmlPage{Title: "Action not confirmed", Message: "Retry the original action; it may already have completed.", RetryAction: name, RetryFields: r.PostForm})
			return
		}
		http.Redirect(w, r, "/actions/results/"+outcome.Nonce, http.StatusSeeOther)
		return
	}
	switch {
	case errors.Is(err, actionengine.ErrExpiredRequestID):
		renderHTML(w, http.StatusGone, htmlPage{Title: "Action expired", Message: "The request expired. Review the game state before starting a new action."})
	case errors.Is(err, actionengine.ErrInvalidRequestID):
		renderHTML(w, http.StatusBadRequest, htmlPage{Title: "Invalid action", Message: "The action request is invalid."})
	case errors.Is(err, actionengine.ErrRequestConflict):
		renderHTML(w, http.StatusConflict, htmlPage{Title: "Action conflict", Message: "This request was already used with different action input."})
	case errors.Is(err, actionengine.ErrUnknownAction):
		renderHTML(w, http.StatusNotFound, htmlPage{Title: "Action unavailable", Message: "This action is no longer available."})
	case errors.Is(err, actionengine.ErrUnknownAccount):
		renderHTML(w, http.StatusUnauthorized, htmlPage{Title: "Sign in required", Message: "Your account could not be verified."})
	default:
		var id [12]byte
		message := "The action was not confirmed. Retry the original action with the same request ID and input."
		if errors.Is(err, actionengine.ErrOutcomeUnknown) {
			message = "The action may have completed. Retry the original action with the same request ID and input."
		}
		if _, randomErr := rand.Read(id[:]); randomErr != nil {
			log.Printf("HTML action error: %v; correlation ID generation: %v", err, randomErr)
		} else {
			correlation := hex.EncodeToString(id[:])
			log.Printf("HTML action error correlation_id=%s: %v", correlation, err)
			message += fmt.Sprintf(" Reference: %s", correlation)
		}
		renderHTML(w, http.StatusServiceUnavailable, htmlPage{Title: "Action not confirmed", Message: message, RetryAction: name, RetryFields: r.PostForm})
	}
}

func (s *Server) handleHTMLResult(w http.ResponseWriter, r *http.Request) {
	ipSlot, status := s.reserveActionIP(r)
	if status != 0 {
		renderHTML(w, status, htmlPage{Title: "Please wait", Message: "Too many requests. Try again later."})
		return
	}
	defer s.finishActionIP(ipSlot)
	account, ok := s.htmlAccount(w, r)
	if !ok {
		return
	}
	userSlot := s.actionUser.reserve(strconv.FormatInt(account.ID, 10))
	if userSlot == nil {
		renderHTML(w, http.StatusTooManyRequests, htmlPage{Title: "Please wait", Message: "Too many requests. Try again later."})
		return
	}
	defer s.actionUser.finishCounted(userSlot)
	service, ok := s.actions.(resultService)
	if !ok {
		renderHTML(w, http.StatusServiceUnavailable, htmlPage{Title: "Temporarily unavailable", Message: "Action results are unavailable."})
		return
	}
	name, outcome, err := service.Result(r.Context(), account.ID, r.PathValue("nonce"))
	if errors.Is(err, actionengine.ErrOutcomeNotFound) {
		renderHTML(w, http.StatusNotFound, htmlPage{Title: "Result unavailable", Message: "The result was not found or has expired. This does not prove that the action failed."})
		return
	}
	if err != nil {
		log.Printf("load HTML action result: %v", err)
		renderHTML(w, http.StatusServiceUnavailable, htmlPage{Title: "Temporarily unavailable", Message: "The action result could not be loaded. Try again."})
		return
	}
	message := ""
	if outcome.Status == "rejected" && outcome.Rejection != nil {
		message = outcome.Rejection.Message
	} else {
		message, err = s.opts.Presentation.Result(name, outcome.Result)
		if err != nil {
			log.Printf("render game action result: %v", err)
			renderHTML(w, http.StatusServiceUnavailable, htmlPage{Title: "Temporarily unavailable", Message: "The action result could not be displayed. Try again."})
			return
		}
	}
	renderHTML(w, http.StatusOK, htmlPage{Title: "Action result", Username: account.Username, Message: message})
}
