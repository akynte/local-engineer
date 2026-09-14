package handler

import (
	"net/http"

	"example.com/shop/internal/service"
)

// UserHandler serves user endpoints.
type UserHandler struct {
	svc *service.UserService
}

func New(svc *service.UserService) *UserHandler { return &UserHandler{svc: svc} }

func (h *UserHandler) Email(w http.ResponseWriter, r *http.Request) {
	email, err := h.svc.Email(r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	_, _ = w.Write([]byte(email))
}
