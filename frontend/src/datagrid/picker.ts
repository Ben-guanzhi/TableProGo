// Tall enough to scan a few dozen keys, short enough that the cell it belongs to
// stays on screen.
export const PICKER_HEIGHT = 264;

// Only one list is ever open, so its rows can be numbered from the position; the
// editor points at the highlighted one with aria-activedescendant.
export const optionId = (index: number) => `fk-option-${index}`;
