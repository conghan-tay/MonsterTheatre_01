"use client";

// Small shared pieces, so every form and button looks and behaves the same.
import { useState, type ChangeEvent, type ComponentProps, type ReactNode } from "react";

export const inputClass =
  "block w-full rounded-md border border-stone-300 bg-white px-3 py-2 text-stone-900 " +
  "placeholder:text-stone-400 focus:border-violet-600 focus:outline-none focus:ring-2 focus:ring-violet-200 " +
  "aria-[invalid=true]:border-red-600";

export function Field({
  id,
  label,
  hint,
  error,
  count,
  children,
}: {
  id: string;
  label: string;
  hint?: string;
  error?: string;
  // How much of the field's limit is used, for a field that has one.
  count?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="space-y-1">
      <label htmlFor={id} className="block text-sm font-medium text-stone-800">
        {label}
      </label>
      {children}
      <div className="flex items-start justify-between gap-4">
        <div>
          {hint && !error && <p className="text-xs text-stone-500">{hint}</p>}
          {error && (
            <p id={`${id}-error`} className="text-sm text-red-700">
              {error}
            </p>
          )}
        </div>
        {count}
      </div>
    </div>
  );
}

// characters counts text as the server does: by character, not by byte or
// by the pairs of code units an emoji takes in a JavaScript string.
export function characters(text: string): number {
  return [...text].length;
}

// CountedField is a text field with a limit: it shows how many characters
// have been typed out of how many are allowed, as "12 / 200".
//
// Pass value and onChange to control it; without them it keeps the text
// itself, as a plain form field does, and only watches the length.
export function CountedField({
  id,
  label,
  hint,
  error,
  max,
  multiline = false,
  value,
  onValue,
  ...input
}: {
  id: string;
  label: string;
  hint?: string;
  error?: string;
  max: number;
  multiline?: boolean;
  value?: string;
  onValue?: (value: string) => void;
  name?: string;
  required?: boolean;
  rows?: number;
  autoComplete?: string;
}) {
  const [typed, setTyped] = useState(0);
  const used = value !== undefined ? characters(value) : typed;
  const shared = {
    id,
    maxLength: max,
    className: inputClass,
    value,
    "aria-invalid": error ? (true as const) : undefined,
    "aria-describedby": error ? `${id}-error` : undefined,
    onChange: (event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => {
      setTyped(characters(event.target.value));
      onValue?.(event.target.value);
    },
    ...input,
  };
  return (
    <Field
      id={id}
      label={label}
      hint={hint}
      error={error}
      count={
        <p className={"shrink-0 text-xs tabular-nums " + (used >= max ? "font-semibold text-stone-900" : "text-stone-500")}>
          {used} / {max}
        </p>
      }
    >
      {multiline ? <textarea {...shared} /> : <input {...shared} />}
    </Field>
  );
}

export function Button({ className = "", ...props }: ComponentProps<"button">) {
  return (
    <button
      {...props}
      className={
        "inline-flex items-center justify-center rounded-md bg-violet-700 px-4 py-2 text-sm font-semibold text-white " +
        "hover:bg-violet-800 focus:outline-none focus-visible:ring-2 focus-visible:ring-violet-400 focus-visible:ring-offset-2 " +
        "disabled:cursor-not-allowed disabled:bg-stone-300 disabled:text-stone-500 " +
        className
      }
    />
  );
}

export function ErrorNotice({ children }: { children: ReactNode }) {
  return (
    <p role="alert" className="rounded-md border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-800">
      {children}
    </p>
  );
}
