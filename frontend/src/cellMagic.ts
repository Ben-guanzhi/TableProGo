// P2-8: the "Query Magics" port — presentation hints a grid cell earns from
// its column name and value, WITHOUT changing the data. Everything here is
// pure and display-only: sorting, copying and editing keep using the raw cell
// text, so a magic can never corrupt what round-trips to the database.

export type CellMagic =
  | { kind: 'link'; url: string; display: string }
  | { kind: 'number'; display: string }
  | { kind: 'date'; display: string }
  | { kind: 'json'; display: string; pretty: string };

// moneyColumn names the amount-ish column words the db-dev magics key off,
// plus the obvious Chinese ones. It is a heuristic on purpose: a wrongly
// formatted number is still the right number, just prettier.
const moneyColumn = /(amount|price|cost|total|fee|balance|salary|money|金额|价格|费用|余额)/i;

const httpUrl = /^https?:\/\/[^\s"'<>]+$/i;

// A plain decimal with no exponent: integers and exact decimals both match,
// whatever kind the wire sent them as.
const plainDecimal = /^(\d+)(?:\.(\d+))?$/;

// isoDateTime matches what the drivers hand over: RFC3339 from the built-ins
// (fractional seconds optional) and "YYYY-MM-DD HH:MM:SS" from the plugin
// path. A date-only value is NOT a datetime magic.
const isoDateTime = /^(\d{4}-\d{2}-\d{2})[T ](\d{2}:\d{2})(:\d{2})(\.\d+)?(Z|[+-]\d{2}:?\d{2})?$/;

// groupThousands inserts separators without ever touching a digit — money
// values arrive as exact decimal text (P3-2) and a Number() round-trip would
// be exactly the loss that kind exists to prevent.
const groupThousands = (digits: string): string => digits.replace(/\B(?=(\d{3})+(?!\d))/g, ',');

// formatMoneyText groups the integer part and fixes the fraction. pad decides
// the fraction's rule: a float64 raw value (a binary approximate anyway) reads
// best as the two cents money always shows, while exact DECIMAL text keeps
// every digit it was given — padding a DECIMAL(10,4)'s 0.1234 to two would
// change what the database stored.
const formatMoneyText = (text: string, pad: boolean): string | null => {
  const sign = text.startsWith('-') ? '-' : '';
  const body = sign ? text.slice(1) : text;
  const m = plainDecimal.exec(body);
  if (!m) return null;
  const whole = groupThousands(m[1]);
  let frac = m[2] ?? '';
  if (pad) {
    frac = (frac + '00').slice(0, 2);
  }
  return frac ? `${sign}${whole}.${frac}` : `${sign}${whole}`;
};

// cellMagic decides, from the raw text the grid already shows, whether a cell
// earns a display form. raw is the unconverted JSON value (number vs string)
// used only to tell numbers from text.
export const cellMagic = (columnName: string, text: string, raw: unknown): CellMagic | null => {
  if (!text) return null;

  // A whole-cell URL becomes a link. Anything else (a paragraph that merely
  // contains one) stays plain text — half-detected links mislead more than
  // missed ones.
  if (httpUrl.test(text)) return { kind: 'link', url: text, display: text };

  if (moneyColumn.test(columnName)) {
    if (typeof raw === 'number' && Number.isFinite(raw)) {
      const display = formatMoneyText(text, true);
      return display ? { kind: 'number', display } : null;
    }
    if (typeof raw === 'string' && plainDecimal.test(text.replace(/^-/, ''))) {
      // Exact decimal text (P3-2 crosses it as a string): keep its digits.
      const display = formatMoneyText(text, false);
      return display ? { kind: 'number', display } : null;
    }
    return null;
  }

  const when = isoDateTime.exec(text);
  if (when) {
    // Drop the machine T-separator and sub-second noise; a non-UTC offset is
    // spelled out (silently rendering in the browser zone would change the
    // value the user reads), and "Z" becomes a visible UTC marker.
    const zone = !when[5] ? '' : when[5] === 'Z' ? ' UTC' : ` ${when[5]}`;
    return { kind: 'date', display: `${when[1]} ${when[2]}${when[3] ?? ''}${zone}` };
  }

  if ((text.startsWith('{') && text.endsWith('}')) || (text.startsWith('[') && text.endsWith(']'))) {
    try {
      const parsed = JSON.parse(text);
      return {
        kind: 'json',
        display: text,
        // The cell keeps the compact line; the badge says "structured" and the
        // tooltip offers this folded form of the same bytes.
        pretty: JSON.stringify(parsed, null, 2),
      };
    } catch {
      return null;
    }
  }
  return null;
};
