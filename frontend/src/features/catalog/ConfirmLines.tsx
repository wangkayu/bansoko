// The confirm dialog's body: two lines, one fact per line (copy guide §1).
export function ConfirmLines({ lines }: { lines: string[] }) {
  return (
    <>
      {lines.map((line) => (
        <p key={line}>{line}</p>
      ))}
    </>
  );
}
