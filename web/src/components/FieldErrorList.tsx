export interface FieldErrorItem {
  message: string;
  path?: string;
}

export interface FieldErrorListProps {
  errors: FieldErrorItem[];
  id?: string;
}

export default function FieldErrorList({ errors, id }: FieldErrorListProps) {
  if (errors.length === 0) {
    return null;
  }

  return (
    <ul className="field-errors" id={id}>
      {errors.map((error, index) => (
        <li key={`${error.path ?? ""}-${index}`}>{error.message}</li>
      ))}
    </ul>
  );
}
