@echo off
chcp 65001 >nul
cd /d "%~dp0"

echo ==========================================
echo  shidianguji downloader
echo ==========================================
echo.
set /p BOOK=Book URL or ID (press Enter for sample):
if "%BOOK%"=="" set BOOK=https://www.shidianguji.com/book/CADAL01023318

echo.
set /p OUTDIR=Output folder (press Enter for "books"):
if "%OUTDIR%"=="" set OUTDIR=books

echo.
echo Downloading: %BOOK%
echo.
sdguji.exe -book "%BOOK%" -out "%OUTDIR%"

echo.
echo Done. Press any key to exit.
pause >nul
