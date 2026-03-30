export function formatUSD(value: number): string {
  const absolute = Math.abs(value);
  const fractionDigits = absolute > 0 && absolute < 0.01 ? 4 : 2;

  return new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: fractionDigits,
    maximumFractionDigits: fractionDigits
  }).format(value);
}
