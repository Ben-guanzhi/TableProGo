// The grid scrolls to reveal the row being edited. That is a nicety, so it
// steps aside when the OS says motion should be reduced.
export function scrollBehavior(): ScrollBehavior {
  const reduce =
    typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches;
  return reduce ? 'auto' : 'smooth';
}
