import "./badges.css";

export function Pill({ children }: { children: string }) {
  return <span className="pill">{children}</span>;
}
