// The shared contract. Everything below depends on this, which is what makes
// it worth asking the graph "what breaks if I change it".
export type Currency = 'GBP' | 'EUR' | 'USD';

export interface Money {
  minor: number;
  currency: Currency;
}

export interface Payment {
  id: string;
  customerId: string;
  amount: Money;
  status: PaymentStatus;
}

export type PaymentStatus = 'pending' | 'settled' | 'refunded';

export interface PaymentRepository {
  find(id: string): Promise<Payment | null>;
  list(customerId: string): Promise<Payment[]>;
}
