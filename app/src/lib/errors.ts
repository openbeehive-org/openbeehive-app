// Errors the UI shows to people. A failure the app knows about carries a code;
// the screen shows the translation of `errors.<code>` instead of the English
// message, which stays for the console and the tests. Anything else is shown
// as a generic sentence in the UI language.

export type ErrorCode =
  | 'not_found'
  | 'db_elsewhere'
  | 'import_unknown_file'
  | 'import_no_export'
  | 'import_bad_json'
  | 'import_not_export'
  | 'import_bad_xml'
  | 'import_csv_empty'
  | 'import_csv_unknown'
  | 'zip_damaged'
  | 'zip_unsupported'
  | 'zip_too_large';

export class CodedError extends Error {
  constructor(
    readonly code: ErrorCode,
    message: string
  ) {
    super(message);
    this.name = 'CodedError';
  }
}

/** Translation key of the message to show for `e`. */
export function errorKey(e: unknown): string {
  if (e instanceof CodedError) return `errors.${e.code}`;
  console.warn('unexpected error:', e);
  return 'errors.generic';
}
