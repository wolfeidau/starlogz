import "./badges.css";

export function Pill({ children }: { children: string }) {
  return (
    <span className="pill" data-category={children}>
      {children}
    </span>
  );
}
