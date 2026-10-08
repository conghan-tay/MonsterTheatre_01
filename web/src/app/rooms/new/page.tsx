"use client";

import { useRouter } from "next/navigation";
import { useState, type FormEvent } from "react";
import { Button, ErrorNotice, Field, inputClass } from "@/components/ui";
import { ApiError, api } from "@/lib/api";

type Slot = "A" | "B";

// The server is the authority on validation. The limits here only mirror its
// rules (docs/system-design.md, section 5) so the browser can stop typing at
// the limit; the server's own messages are what get shown.
const LIMITS = { name: 80, brief: 400, topic: 200, description: 1000, genre: 80 };
const MAX_PORTRAIT_BYTES = 5 * 1024 * 1024;

export default function NewRoomPage() {
  const router = useRouter();
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const text = (name: string) => String(data.get(name) ?? "");
    const file = (name: string) => data.get(name) as File | null;

    // Checked here only because the server cannot say which file was too big.
    const tooBig: Record<string, string> = {};
    for (const slot of ["A", "B"] as Slot[]) {
      const portrait = file(`portrait${slot}`);
      if (portrait && portrait.size > MAX_PORTRAIT_BYTES) tooBig[`portrait${slot}`] = "Must be at most 5 MiB.";
    }
    if (Object.keys(tooBig).length > 0) {
      setFieldErrors(tooBig);
      setFormError("Some fields need attention.");
      return;
    }

    const form = new FormData();
    form.append(
      "room",
      new Blob(
        [
          JSON.stringify({
            scene: { topic: text("topic"), description: text("description"), genre: text("genre") },
            characterA: { name: text("nameA"), brief: text("briefA") },
            characterB: { name: text("nameB"), brief: text("briefB") },
          }),
        ],
        { type: "application/json" },
      ),
    );
    for (const slot of ["A", "B"] as Slot[]) {
      const portrait = file(`portrait${slot}`);
      if (portrait && portrait.size > 0) form.append(`portrait${slot}`, portrait);
    }

    setBusy(true);
    setFieldErrors({});
    setFormError(null);
    try {
      const room = await api.createRoom(form);
      router.push(`/rooms/${room.roomId}`);
    } catch (err) {
      setBusy(false);
      if (err instanceof ApiError) {
        if (err.status === 401) return router.replace("/login");
        setFieldErrors(err.fields);
        setFormError(err.message);
      } else {
        setFormError("Something went wrong. Try again.");
      }
    }
  }

  const invalid = (field: string) => ({
    "aria-invalid": fieldErrors[field] ? true : undefined,
    "aria-describedby": fieldErrors[field] ? `${field}-error` : undefined,
  });

  function character(slot: Slot) {
    const name = `character${slot}.name`;
    const brief = `character${slot}.brief`;
    const portrait = `portrait${slot}`;
    return (
      <fieldset className="space-y-4 rounded-md border border-stone-200 bg-white p-4">
        <legend className="px-1 text-sm font-semibold">Character {slot}</legend>
        <Field id={name} label="Name" error={fieldErrors[name]}>
          <input id={name} name={`name${slot}`} required maxLength={LIMITS.name} className={inputClass} {...invalid(name)} />
        </Field>
        <Field id={brief} label="Brief" hint="Optional. A sentence or two about who they are." error={fieldErrors[brief]}>
          <textarea id={brief} name={`brief${slot}`} rows={2} maxLength={LIMITS.brief} className={inputClass} {...invalid(brief)} />
        </Field>
        <Field id={portrait} label="Portrait" hint="JPEG, PNG, or WebP, up to 5 MiB." error={fieldErrors[portrait]}>
          <input
            id={portrait}
            name={portrait}
            type="file"
            accept="image/jpeg,image/png,image/webp"
            required
            className="block w-full text-sm text-stone-700 file:mr-3 file:rounded-md file:border-0 file:bg-stone-200 file:px-3 file:py-2 file:text-sm file:font-medium hover:file:bg-stone-300"
            {...invalid(portrait)}
          />
        </Field>
      </fieldset>
    );
  }

  return (
    <>
      <h1 className="text-2xl font-bold">New room</h1>
      <p className="mt-1 text-sm text-stone-600">
        A room cannot be edited after it is created. To change anything, create another room.
      </p>

      <form onSubmit={submit} className="mt-6 space-y-6" noValidate>
        {formError && <ErrorNotice>{formError}</ErrorNotice>}

        <fieldset className="space-y-4 rounded-md border border-stone-200 bg-white p-4">
          <legend className="px-1 text-sm font-semibold">Scene</legend>
          <Field id="scene.topic" label="Topic" hint="What the scene is about, in a sentence." error={fieldErrors["scene.topic"]}>
            <input id="scene.topic" name="topic" required maxLength={LIMITS.topic} className={inputClass} {...invalid("scene.topic")} />
          </Field>
          <Field id="scene.description" label="Description" hint="Optional. Where and when it happens." error={fieldErrors["scene.description"]}>
            <textarea id="scene.description" name="description" rows={3} maxLength={LIMITS.description} className={inputClass} {...invalid("scene.description")} />
          </Field>
          <Field id="scene.genre" label="Genre" hint="Optional. For example: comedy." error={fieldErrors["scene.genre"]}>
            <input id="scene.genre" name="genre" maxLength={LIMITS.genre} className={inputClass} {...invalid("scene.genre")} />
          </Field>
        </fieldset>

        <div className="grid gap-6 sm:grid-cols-2">
          {character("A")}
          {character("B")}
        </div>

        <Button type="submit" disabled={busy}>
          {busy ? "Creating…" : "Create room"}
        </Button>
      </form>
    </>
  );
}
