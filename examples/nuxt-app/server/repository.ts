import type { Payment, PaymentRepository } from '~/types/payment';

// An in-memory implementation, so the example runs with no database. The
// interface is what the graph records: changing PaymentRepository must find
// this class.
export class MemoryPaymentRepository implements PaymentRepository {
  private readonly payments = new Map<string, Payment>();

  async find(id: string): Promise<Payment | null> {
    return this.payments.get(id) ?? null;
  }

  async list(customerId: string): Promise<Payment[]> {
    return [...this.payments.values()].filter((p) => p.customerId === customerId);
  }

  add(payment: Payment): void {
    this.payments.set(payment.id, payment);
  }
}
