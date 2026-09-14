package api

import "example.com/billing/internal/billing"

// InvoiceResponse is the JSON shape returned to clients.
type InvoiceResponse struct {
	TotalPence int `json:"total_pence"`
}

// Build turns lines into a response.
func Build(lines []billing.Line) InvoiceResponse {
	return InvoiceResponse{TotalPence: billing.Total(lines)}
}
