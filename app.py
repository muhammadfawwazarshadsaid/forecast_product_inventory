from flask import Flask, request, jsonify
from google.oauth2 import service_account
from googleapiclient.discovery import build
import numpy as np
import pandas as pd
import os

app = Flask(__name__)

CREDENTIALS_FILE = "credentials.json"
SCOPES = ["https://www.googleapis.com/auth/spreadsheets"]

def get_sheets_service():
    creds = service_account.Credentials.from_service_account_file(
        CREDENTIALS_FILE, scopes=SCOPES
    )
    return build("sheets", "v4", credentials=creds)

def linear_forecast(row, anchor):
    past_cols = [
        f"{m}-{y}"
        for y in ["24", "25"]
        for m in ["Jan","Feb","Mar","Apr","May","Jun","Jul","Aug","Sep","Oct","Nov","Dec"]
    ]
    forecast_cols = [
        "Jan-26","Feb-26","Mar-26","Apr-26","May-26","Jun-26",
        "Jul-26","Aug-26","Sep-26","Oct-26","Nov-26","Dec-26"
    ]
    vals = row[past_cols].dropna().values
    if len(vals) < 2:
        return row
    x = np.arange(len(vals))
    y = vals
    slope, intercept = np.polyfit(x, y, 1)
    future_x = np.arange(len(vals), len(vals) + 12)
    preds = intercept + slope * future_x
    dec26_pred = preds[-1]
    if dec26_pred != 0 and anchor > 0:
        preds *= (anchor / dec26_pred)
    for i, col in enumerate(forecast_cols[:-1]):
        row[col] = round(preds[i], 2)
    return row

@app.route("/forecast", methods=["POST"])
def forecast():
    body = request.get_json(force=True)
    spreadsheet_id = body.get("spreadsheetId")
    sheet_name = body.get("sheetName", "Inventory Simulation")
    service = get_sheets_service()
    RANGE_READ = f"{sheet_name}!B54:AM62"
    RANGE_WRITE = f"{sheet_name}!AB54:AL62"
    result = service.spreadsheets().values().get(
        spreadsheetId=spreadsheet_id, range=RANGE_READ
    ).execute()
    values = result.get("values", [])
    if not values:
        return jsonify({"error": "no data found"}), 400
    headers = [
        "Product","Remark",
        *[f"{m}-{y}" for y in ["24","25","26"] for m in ["Jan","Feb","Mar","Apr","May","Jun","Jul","Aug","Sep","Oct","Nov","Dec"]]
    ]
    df = pd.DataFrame(values, columns=headers[: len(values[0])])
    for col in headers[2:]:
        df[col] = pd.to_numeric(df[col], errors="coerce")
    anchor_row = df.loc[df["Remark"] == "TOTAL DIN Yearly"]
    anchor = float(anchor_row["Dec-26"].values[0]) if not anchor_row.empty else 0.0
    for idx, row in df.iterrows():
        if row["Remark"] in ["COGS (K-EUR)", "TOTAL DIN Yearly"]:
            continue
        df.loc[idx] = linear_forecast(row, anchor)
    out_values = df[
        ["Jan-26","Feb-26","Mar-26","Apr-26","May-26","Jun-26",
         "Jul-26","Aug-26","Sep-26","Oct-26","Nov-26"]
    ].fillna("").values.tolist()
    service.spreadsheets().values().update(
        spreadsheetId=spreadsheet_id,
        range=RANGE_WRITE,
        valueInputOption="RAW",
        body={"values": out_values},
    ).execute()
    return jsonify({"status": "✅ Forecast updated successfully", "anchor": anchor})

if __name__ == "__main__":
    port = int(os.environ.get("PORT", 8080))
    print(f"🚀 Running on port {port}")
    app.run(host="0.0.0.0", port=port)
