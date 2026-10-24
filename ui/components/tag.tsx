import "./badges.css";

export function Tag({ children }: { children: string }) {
  return <span className="tag">{children}</span>;
}
