package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/The-FKing-Projects/StillWorld/internal/actiondata"
	"github.com/The-FKing-Projects/StillWorld/internal/actionengine"
	"github.com/The-FKing-Projects/StillWorld/internal/auth"
)

type actionAPIError struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	CorrelationID string `json:"correlation_id,omitempty"`
}

func (s *Server) handleIssueActionRequest(w http.ResponseWriter, r *http.Request) {
	ipSlot, status := s.reserveActionIP(r)
	if status != 0 {
		writeActionAdmissionError(w, status)
		return
	}
	defer s.finishActionIP(ipSlot)

	account, ok := s.authenticateAction(w, r)
	if !ok {
		return
	}
	userSlot := s.actionUser.reserve(strconv.FormatInt(account.ID, 10))
	if userSlot == nil {
		writeActionError(w, http.StatusTooManyRequests, "rate_limited", "Too many action requests; try again later.", "")
		return
	}
	defer s.actionUser.finishCounted(userSlot)

	requestID, err := s.actions.IssueRequestID(r.Context(), account.ID, r.PathValue("actionName"))
	switch {
	case err == nil:
		writeActionJSON(w, http.StatusOK, struct {
			RequestID string `json:"request_id"`
			ExpiresAt string `json:"expires_at"`
		}{RequestID: requestID.Token, ExpiresAt: requestID.ExpiresAt.UTC().Format(time.RFC3339Nano)})
	case errors.Is(err, actionengine.ErrUnknownAction):
		writeActionError(w, http.StatusNotFound, "unknown_action", "The requested action is not available.", "")
	case errors.Is(err, actionengine.ErrUnknownAccount):
		writeActionError(w, http.StatusUnauthorized, "not_authenticated", "Authentication is required.", "")
	default:
		writeActionInternalError(w, err, "temporary_error", "The request ID could not be issued. Try again.")
	}
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	ipSlot, status := s.reserveActionIP(r)
	if status != 0 {
		writeActionAdmissionError(w, status)
		return
	}
	defer s.finishActionIP(ipSlot)

	account, ok := s.authenticateAction(w, r)
	if !ok {
		return
	}
	userSlot := s.actionUser.reserve(strconv.FormatInt(account.ID, 10))
	if userSlot == nil {
		writeActionError(w, http.StatusTooManyRequests, "rate_limited", "Too many action requests; try again later.", "")
		return
	}
	defer s.actionUser.finishCounted(userSlot)

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeActionError(w, http.StatusUnsupportedMediaType, "json_required", "Send one JSON object as the request body.", "")
		return
	}
	idempotencyKeys := r.Header.Values("Idempotency-Key")
	if len(idempotencyKeys) != 1 || idempotencyKeys[0] == "" ||
		strings.TrimSpace(idempotencyKeys[0]) != idempotencyKeys[0] {
		writeActionError(w, http.StatusBadRequest, "request_id_required", "Provide one Idempotency-Key request ID.", "")
		return
	}
	input, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeActionError(w, http.StatusRequestEntityTooLarge, "input_too_large", "The action input is too large.", "")
			return
		}
		writeActionInternalError(w, err, "retry_same_request_id", "The action could not be read. Retry with the same request ID.")
		return
	}
	if err := actiondata.ValidateInput(input); err != nil {
		if errors.Is(err, actiondata.ErrInputTooLarge) {
			writeActionError(w, http.StatusRequestEntityTooLarge, "input_too_large", "The action input is too large.", "")
		} else {
			writeActionError(w, http.StatusBadRequest, "invalid_input", "Send one valid JSON object without duplicate keys.", "")
		}
		return
	}

	outcome, err := s.actions.Execute(r.Context(), account.ID, r.PathValue("actionName"), idempotencyKeys[0], input)
	if err != nil {
		writeActionEngineError(w, err)
		return
	}
	status = http.StatusOK
	if outcome.Status == "rejected" {
		status = http.StatusUnprocessableEntity
	}
	writeActionJSON(w, status, outcome)
}

func (s *Server) authenticateAction(w http.ResponseWriter, r *http.Request) (auth.Account, bool) {
	cookie, err := r.Cookie(s.cookieName)
	if err != nil {
		writeActionError(w, http.StatusUnauthorized, "not_authenticated", "Authentication is required.", "")
		return auth.Account{}, false
	}
	account, err := s.auth.Authenticate(r.Context(), cookie.Value)
	switch {
	case err == nil:
		return account, true
	case errors.Is(err, auth.ErrNoSession):
		s.setCookie(w, "", -1)
		writeActionError(w, http.StatusUnauthorized, "not_authenticated", "Authentication is required.", "")
	default:
		writeActionInternalError(w, err, "temporary_error", "Authentication could not be verified. Try again.")
	}
	return auth.Account{}, false
}

func (s *Server) reserveActionIP(r *http.Request) (*entry, int) {
	select {
	case s.actionSlots <- struct{}{}:
	default:
		return nil, http.StatusServiceUnavailable
	}
	slot := s.actionIP.reserve(s.clientIP(r))
	if slot == nil {
		<-s.actionSlots
		return nil, http.StatusTooManyRequests
	}
	return slot, 0
}

func (s *Server) finishActionIP(slot *entry) {
	s.actionIP.finishCounted(slot)
	<-s.actionSlots
}

func writeActionAdmissionError(w http.ResponseWriter, status int) {
	if status == http.StatusTooManyRequests {
		writeActionError(w, status, "rate_limited", "Too many action requests; try again later.", "")
		return
	}
	writeActionError(w, status, "server_busy", "The action service is busy.", "")
}

func writeActionEngineError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, actionengine.ErrUnknownAction):
		writeActionError(w, http.StatusNotFound, "unknown_action", "The requested action is not available.", "")
	case errors.Is(err, actionengine.ErrUnknownAccount):
		writeActionError(w, http.StatusUnauthorized, "not_authenticated", "Authentication is required.", "")
	case errors.Is(err, actionengine.ErrInvalidRequestID):
		writeActionError(w, http.StatusBadRequest, "invalid_request_id", "The request ID is invalid for this action.", "")
	case errors.Is(err, actionengine.ErrExpiredRequestID):
		writeActionError(w, http.StatusGone, "request_id_expired", "Refresh the action before submitting it again.", "")
	case errors.Is(err, actionengine.ErrRequestConflict):
		writeActionError(w, http.StatusConflict, "request_id_conflict", "This request ID was already used with different action input.", "")
	case errors.Is(err, actionengine.ErrOutcomeUnknown):
		writeActionInternalError(w, err, "outcome_unknown", "The action may have completed. Retry the original request with the same request ID and identical input.")
	case errors.Is(err, actiondata.ErrInputTooLarge):
		writeActionError(w, http.StatusRequestEntityTooLarge, "input_too_large", "The action input is too large.", "")
	case errors.Is(err, actiondata.ErrInvalidInput):
		writeActionError(w, http.StatusBadRequest, "invalid_input", "Send one valid JSON object without duplicate keys.", "")
	default:
		writeActionInternalError(w, err, "retry_same_request_id", "The action was not confirmed. Retry the original request with the same request ID and identical input.")
	}
}

func writeActionInternalError(w http.ResponseWriter, err error, code, message string) {
	var randomID [12]byte
	if _, randomErr := rand.Read(randomID[:]); randomErr != nil {
		log.Printf("action API internal error: %v; correlation ID generation failed: %v", err, randomErr)
		writeActionError(w, http.StatusServiceUnavailable, code, message, "")
		return
	}
	correlationID := hex.EncodeToString(randomID[:])
	log.Printf("action API internal error correlation_id=%s: %v", correlationID, err)
	writeActionError(w, http.StatusServiceUnavailable, code, message, correlationID)
}

func writeActionError(w http.ResponseWriter, status int, code, message, correlationID string) {
	writeActionJSON(w, status, actionAPIError{
		Code:          code,
		Message:       message,
		CorrelationID: correlationID,
	})
}

func writeActionJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write action API response: encode response: %v", err)
	}
}

var _ ActionService = (*actionengine.Engine)(nil)
