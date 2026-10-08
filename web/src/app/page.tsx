import { redirect } from "next/navigation";

// There is no landing page yet; directors go straight to their rooms, and the
// rooms pages send anyone not logged in to /login.
export default function Home() {
  redirect("/rooms");
}
