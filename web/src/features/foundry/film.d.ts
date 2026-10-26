/** Mounts the ASCII film into `root` (the markup from FoundryPage); the result stops it again. */
export function mountFilm(
  root: HTMLElement,
  labels?: { play?: string; pause?: string; chapter?: (n: number, name: string) => string; noFullscreen?: string },
): () => void;
