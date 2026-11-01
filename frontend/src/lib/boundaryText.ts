// The text the error boundary shows for a thrown value. Kept in a .ts module so
// the tests can reach it without a JSX transform (node --test loads .ts only).

// boundaryDetail renders any thrown value as one line of text. The boundary is
// the last line of defence, so it must not throw while describing the throw.
export function boundaryDetail(error: unknown): string {
  if (error instanceof Error) return error.message;
  try {
    return String(error);
  } catch {
    return 'unprintable error';
  }
}
