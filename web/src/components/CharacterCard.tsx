import type { Character } from "@/lib/api";

// One of the room's two characters. speaking marks the one whose line it
// is: being chosen during a turn, or being heard at the premiere.
export function CharacterCard({
  slot,
  character,
  speaking = false,
}: {
  slot: string;
  character: Character;
  speaking?: boolean;
}) {
  return (
    <article
      aria-current={speaking ? "true" : undefined}
      className={
        "flex gap-4 rounded-md border bg-white p-4 transition-shadow " +
        (speaking ? "border-violet-600 ring-2 ring-violet-300" : "border-stone-200")
      }
    >
      {/* A plain img: the portrait is private, served by the API with the
          session cookie, and already the size it was uploaded at. */}
      {/* eslint-disable-next-line @next/next/no-img-element */}
      <img
        src={character.portraitUrl}
        alt={`Portrait of ${character.name}`}
        width={96}
        height={96}
        className="h-24 w-24 shrink-0 rounded-md bg-stone-100 object-cover"
      />
      <div>
        <p className="text-xs uppercase tracking-wide text-stone-500">Character {slot}</p>
        <h2 className="text-lg font-semibold">{character.name}</h2>
        {character.brief && <p className="mt-1 text-sm text-stone-700">{character.brief}</p>}
      </div>
    </article>
  );
}
