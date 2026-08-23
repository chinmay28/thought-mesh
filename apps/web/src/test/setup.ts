import '@testing-library/jest-dom/vitest';
import { afterEach } from 'vitest';
import { cleanup } from '@testing-library/react';

// With Vitest globals off, Testing Library can't auto-register its cleanup via
// a global `afterEach`, so renders would accumulate across tests. Do it here.
afterEach(cleanup);
