// Command api is the service entry point.
package main

import (
	"log"
	"net/http"

	"example.com/payments/internal/config"
	"example.com/payments/internal/handler"
	"example.com/payments/internal/repository"
	"example.com/payments/internal/service"
)

func main() {
	cfg := config.Load()

	store := repository.NewPostgresStore(cfg.DatabaseURL)
	svc := service.NewPaymentService(store)
	h := handler.NewPaymentHandler(svc)

	mux := http.NewServeMux()
	h.Register(mux)

	log.Printf("listening on %s (%s)", cfg.ListenAddr, cfg.Environment)
	if err := http.ListenAndServe(cfg.ListenAddr, mux); err != nil {
		log.Fatal(err)
	}
}
