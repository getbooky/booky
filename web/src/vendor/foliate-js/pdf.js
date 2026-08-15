// Booky stub: upstream pdf.js pulls in a 13 MB pdfjs vendor tree; Booky's
// reader doesn't offer PDFs, so the loader path stays resolvable without it.
export const makePDF = () => {
    throw new Error('PDF reading is not supported')
}
