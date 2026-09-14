// Package send builds customer messages. It is the second consumer of the
// shared library, in a different module from the first.
package send

import (
	"fmt"

	"example.com/libs/money"
)

// ReceiptBody renders the body of a receipt.
func ReceiptBody(customer string, charged money.Amount) string {
	return fmt.Sprintf("Hello %s, we have charged you %s.", customer, charged.String())
}
