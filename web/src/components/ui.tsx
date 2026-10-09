// Small shared pieces, so every form and button looks and behaves the same.
import type { ComponentProps, ReactNode } from "react";

export const inputClass =
  "block w-full rounded-md border border-stone-300 bg-white px-3 py-2 text-stone-900 " +
  "placeholder:text-stone-400 focus:border-violet-600 focus:outline-none focus:ring-2 focus:ring-violet-200 " +
  "aria-[invalid=true]:border-red-600";

export function Field({
  id,
  label,
  hint,
  error,
  children,
}: {
  id: string;
  label: string;
  hint?: string;
  error?: string;
  children: ReactNode;
}) {
  return (
    <div className="space-y-1">
      <label htmlFor={id} className="block text-sm font-medium text-stone-800">
        {label}
      </label>
      {children}
      {hint && !error && <p className="text-xs text-stone-500">{hint}</p>}
      {error && (
        <p id={`${id}-error`} className="text-sm text-red-700">
          {error}
        </p>
      )}
    </div>
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
