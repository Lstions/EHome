/**
 * Unpack API response - handles both envelope {code, data, message} and bare responses.
 * The axios interceptor already strips the HTTP envelope (response.data → parsed body),
 * so this handles the business-level envelope where present.
 */
export function unpackResponse<T>(response: any): T {
  if (response?.data !== undefined) return response.data as T
  return response as T
}
