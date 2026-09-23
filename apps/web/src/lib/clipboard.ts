/**
 * Put text on the clipboard.
 *
 * The async Clipboard API only exists in a secure context — HTTPS or
 * localhost — and a self-hosted server is usually reached over plain HTTP on a
 * LAN address, where `navigator.clipboard` is simply undefined. The fallback is
 * the old select-and-`execCommand('copy')` dance on an off-screen textarea,
 * which every browser still honours from inside a user gesture.
 */
export async function copyText(text: string): Promise<void> {
  if (window.isSecureContext && navigator.clipboard?.writeText) {
    await navigator.clipboard.writeText(text);
    return;
  }
  const area = document.createElement('textarea');
  area.value = text;
  area.setAttribute('readonly', '');
  // Off-screen but not display:none — a hidden element can't be selected.
  area.style.position = 'fixed';
  area.style.top = '-1000px';
  area.style.opacity = '0';
  document.body.appendChild(area);
  try {
    area.select();
    area.setSelectionRange(0, text.length); // iOS ignores select() alone
    if (!document.execCommand('copy')) throw new Error('This browser refused to copy.');
  } finally {
    area.remove();
  }
}
