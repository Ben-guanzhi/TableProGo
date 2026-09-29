// The async clipboard refuses a window that is not focused - which is exactly
// what a background tab is - so fall back to the selection copy that still
// works there. Returns false when neither route got the text across.
export const copyText = async (text: string): Promise<boolean> => {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const box = document.createElement('textarea');
    box.value = text;
    box.style.position = 'fixed';
    box.style.top = '0';
    box.style.opacity = '0';
    document.body.appendChild(box);
    box.select();
    let copied = false;
    try {
      copied = document.execCommand('copy');
    } catch {
      copied = false;
    }
    box.remove();
    return copied;
  }
};
