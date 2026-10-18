"use client";

import { useState } from "react";
import { createPortal } from "react-dom";

// Stage is what the premiere looks like while a line is heard: the speaker's
// portrait, as large as the window allows at its own shape, and the line
// beneath it, over a darkened, blurred page (docs/decisions.md, D054).
//
// The speaker's name is not shown; the portrait's alternative text says it.
// Each new line fades in, keyed by its turn, unless the browser asks for
// reduced motion.
//
// It is drawn straight into the body, so that no page around it (a spacing
// rule's margins, say) can move or shrink it. It is only ever shown during
// playback, in the browser, so there is always a body to draw into.
export function Stage({
  turn,
  name,
  portraitUrl,
  line,
}: {
  turn: number;
  name: string;
  portraitUrl: string;
  line?: string;
}) {
  return createPortal(
    <section
      aria-label="Stage"
      aria-live="polite"
      className="fixed inset-0 z-50 flex items-center justify-center bg-stone-950/95 px-4 pt-[max(1.5rem,env(safe-area-inset-top))] pb-[max(1.5rem,env(safe-area-inset-bottom))] backdrop-blur-md sm:px-8"
    >
      <div key={turn} className="flex w-full flex-col items-center gap-6 motion-safe:animate-[stage-in_300ms_ease-out]">
        <Portrait key={portraitUrl} url={portraitUrl} name={name} />
        {line && (
          <p className="max-h-36 max-w-3xl overflow-y-auto text-center text-xl leading-snug text-stone-50 sm:text-3xl">
            {line}
          </p>
        )}
      </div>
    </section>,
    document.body,
  );
}

// Height kept free of the portrait: the stage's padding, the gap, and the
// line's own most (max-h-36).
const RESERVED = "15rem";

// Portrait is drawn at its own shape: as wide as the stage, or as tall as the
// window less the room kept for the line, whichever comes first. It is
// scaled up if it is smaller than that.
function Portrait({ url, name }: { url: string; name: string }) {
  // Width over height, known once the image has loaded. The character cards
  // have already loaded it, so that is at once.
  const [shape, setShape] = useState<number | null>(null);
  return (
    // A plain img: the portrait is private, served by the API with the
    // session cookie.
    // eslint-disable-next-line @next/next/no-img-element
    <img
      src={url}
      alt={`Portrait of ${name}`}
      onLoad={(e) => setShape(e.currentTarget.naturalWidth / e.currentTarget.naturalHeight)}
      className={"object-contain " + (shape === null ? "invisible" : "")}
      style={{
        aspectRatio: shape ?? 1,
        width: `min(100%, calc((100dvh - ${RESERVED}) * ${shape ?? 1}))`,
      }}
    />
  );
}
