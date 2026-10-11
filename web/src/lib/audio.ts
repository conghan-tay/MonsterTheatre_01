// The one audio element the premiere is played through.
//
// A browser lets a page make sound only after the person has clicked on it.
// Chrome then allows any later playback on that page; Safari allows it for
// the element that was played during the click. So there is one element,
// it is started inside the click (Enter theatre, or Start), and every clip
// is played through it afterwards.

let element: HTMLAudioElement | null = null;
let silence: string | null = null;

export function audioElement(): HTMLAudioElement {
  return (element ??= new Audio());
}

// A twentieth of a second of silence, as a WAV file made here: 8-bit mono
// at 8 kHz, where the value 128 is no sound.
function silentClip(): string {
  if (silence) return silence;
  const samples = 400;
  const bytes = new Uint8Array(44 + samples).fill(128, 44);
  const view = new DataView(bytes.buffer);
  const text = (offset: number, value: string) => [...value].forEach((c, i) => view.setUint8(offset + i, c.charCodeAt(0)));
  text(0, "RIFF");
  view.setUint32(4, 36 + samples, true);
  text(8, "WAVEfmt ");
  view.setUint32(16, 16, true); // size of the format block
  view.setUint16(20, 1, true); // PCM
  view.setUint16(22, 1, true); // one channel
  view.setUint32(24, 8000, true); // samples a second
  view.setUint32(28, 8000, true); // bytes a second
  view.setUint16(32, 1, true); // bytes a sample
  view.setUint16(34, 8, true); // bits a sample
  text(36, "data");
  view.setUint32(40, samples, true);
  return (silence = URL.createObjectURL(new Blob([bytes], { type: "audio/wav" })));
}

// unlockAudio must be called from inside a click handler, before anything
// is awaited. It plays the silent clip, which is what earns the element the
// right to play the recordings later without another click.
export function unlockAudio(): void {
  const audio = audioElement();
  audio.src = silentClip();
  // If this is refused, the premiere shows "Tap to hear audio" instead.
  audio.play().catch(() => undefined);
}
