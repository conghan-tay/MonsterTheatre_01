import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Monster Theatre",
  description: "An audience votes on the dialogue for two characters, then hears the performance they wrote.",
};

export default function RootLayout({ children }: LayoutProps<"/">) {
  return (
    <html lang="en">
      <body className="min-h-screen bg-stone-50 text-stone-900 antialiased">{children}</body>
    </html>
  );
}
