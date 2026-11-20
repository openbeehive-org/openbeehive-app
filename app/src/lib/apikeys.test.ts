import { describe, expect, it } from 'vitest';
import { API_DOCS_URL, apiBaseUrl, curlExample } from './apikeys';

describe('apiBaseUrl', () => {
  it('is the app address unless the API lives elsewhere', () => {
    expect(apiBaseUrl('', 'https://bees.example.org')).toBe('https://bees.example.org');
    expect(apiBaseUrl('https://api.example.org/', 'https://bees.example.org')).toBe(
      'https://api.example.org'
    );
  });
});

describe('curlExample', () => {
  const key = 'key-shown-once';
  const lines = curlExample('https://bees.example.org', key).split(' \\\n');

  it('is one command of four lines', () => {
    expect(lines).toHaveLength(4);
    expect(lines[0]).toBe(
      'curl -X POST https://bees.example.org/openbeehive.v1.HiveService/ListHives'
    );
    expect(lines[2]).toBe('  -H "Content-Type: application/json"');
    expect(lines[3]).toBe("  -d '{}'");
  });

  it('carries the key as a bearer token', () => {
    expect(lines[1]).toBe(['  -H "Authorization:', 'Bearer', `${key}"`].join(' '));
  });

  it('links to documentation on the project site', () => {
    expect(API_DOCS_URL).toMatch(/^https:\/\/docs\.openbeehive\.org\//);
  });
});
